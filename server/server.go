package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/elazarl/go-bindata-assetfs"
	"github.com/gorilla/websocket"
	"github.com/pkg/errors"

	"github.com/yudai/gotty/backend"
	"github.com/yudai/gotty/pkg/homedir"
	"github.com/yudai/gotty/pkg/randomstring"
)

type Server struct {
	factory backend.Factory
	options *Options

	srv *http.Server

	ctx    context.Context
	cancel context.CancelFunc

	upgrader *websocket.Upgrader

	timer       *time.Timer
	wsWG        sync.WaitGroup
	url         *url.URL // use URL()
	connections *int64   // Use atomic operations
	once        *int64   // use atomic operations
}

func New(factory backend.Factory, options *Options) (*Server, error) {
	connections := int64(0)
	once := int64(0)
	return &Server{
		factory: factory,
		options: options,

		upgrader: &websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			Subprotocols:    []string{"gotty"},
		},
		connections: &connections,
		once:        &once,
	}, nil
}

func (server *Server) Run(ctx context.Context) error {
	handler := server.setupHandlers()
	srv, err := server.setupHTTPServer(handler)
	if err != nil {
		return errors.Wrapf(err, "failed to setup an HTTP server")
	}

	if server.options.PermitWrite {
		log.Printf("Permitting clients to write input to the PTY.")
	}

	if server.options.Once {
		log.Printf("Once option is provided, accepting only one client")
	}

	server.srv = srv
	cctx, cancel := context.WithCancel(ctx)
	server.ctx = cctx
	server.cancel = cancel

	if server.options.Timeout > 0 {
		server.timer = time.NewTimer(time.Duration(server.options.Timeout) * time.Second)
		go func() {
			select {
			case <-server.timer.C:
				server.shutdown()
			case <-cctx.Done():
			}
		}()
	}

	go func() {
		if server.options.EnableTLS {
			crtFile := homedir.Expand(server.options.TLSCrtFile)
			keyFile := homedir.Expand(server.options.TLSKeyFile)
			log.Printf("TLS crt file: " + crtFile)
			log.Printf("TLS key file: " + keyFile)

			err = srv.ListenAndServeTLS(crtFile, keyFile)
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil {
			//
		}
	}()

	<-cctx.Done()
	srv.Shutdown(context.Background())
	log.Printf("Waiting for connections to be closed")
	server.wsWG.Wait()

	return nil
}

func (server *Server) shutdown() {
	server.cancel()
}

func (server *Server) setupHandlers() http.Handler {
	staticFileHandler := http.FileServer(
		&assetfs.AssetFS{Asset: Asset, AssetDir: AssetDir, Prefix: "static"},
	)

	url := server.URL()
	var siteMux = http.NewServeMux()
	if server.options.IndexFile != "" {
		log.Printf("Using index file at " + server.options.IndexFile)
		siteMux.HandleFunc(url.Path, server.handleCustomIndex)
	} else {
		siteMux.Handle(url.Path, http.StripPrefix(url.Path, staticFileHandler))
	}
	siteMux.Handle(url.Path+"js/", http.StripPrefix(url.Path, staticFileHandler))
	siteMux.Handle(url.Path+"favicon.png", http.StripPrefix(url.Path, staticFileHandler))
	siteMux.HandleFunc(url.Path+"auth_token.js", server.handleAuthToken)

	siteHandler := http.Handler(siteMux)

	if server.options.EnableBasicAuth {
		log.Printf("Using Basic Authentication")
		siteHandler = server.wrapBasicAuth(siteHandler, server.options.Credential)
	}

	siteHandler = server.wrapHeaders(siteHandler)

	wsMux := http.NewServeMux()
	wsMux.Handle("/", siteHandler)
	wsMux.HandleFunc(url.Path+"ws", server.handleWS)
	siteHandler = http.Handler(wsMux)

	return server.wrapLogger(siteHandler)
}

func (server *Server) setupHTTPServer(handler http.Handler) (*http.Server, error) {
	url := server.URL()
	log.Printf("URL: %s", url.String())

	srv := &http.Server{
		Addr:    url.Host,
		Handler: handler,
	}

	if server.options.EnableTLSClientAuth {
		tlsConfig, err := server.tlsConfig()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to setup TLS configuration")
		}
		srv.TLSConfig = tlsConfig
	}

	return srv, nil
}

func (server *Server) URL() *url.URL {
	if server.url == nil {
		host := net.JoinHostPort(server.options.Address, server.options.Port)
		path := ""
		if server.options.EnableRandomUrl {
			path += "/" + randomstring.Generate(server.options.RandomUrlLength)
		}
		scheme := "http"
		if server.options.EnableTLS {
			scheme = "https"
		}
		server.url = &url.URL{Scheme: scheme, Host: host, Path: path + "/"}
	}
	return server.url
}

func (server *Server) tlsConfig() (*tls.Config, error) {
	caFile := homedir.Expand(server.options.TLSCACrtFile)
	caCert, err := ioutil.ReadFile(caFile)
	if err != nil {
		return nil, errors.New("Could not open CA crt file " + caFile)
	}
	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, errors.New("Could not parse CA crt file data in " + caFile)
	}
	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}
	return tlsConfig, nil
}
