// This file is safe to edit. Once it exists it will not be overwritten

// Copyright 2022 The Sigstore Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package restapi

import (
	"crypto/tls"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/middleware"
	"github.com/go-openapi/errors"
	"github.com/go-openapi/runtime"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/cors"
	"github.com/urfave/negroni"

	pkgapi "github.com/sigstore/timestamp-authority/pkg/api"
	"github.com/sigstore/timestamp-authority/pkg/generated/restapi/operations"
	"github.com/sigstore/timestamp-authority/pkg/generated/restapi/operations/timestamp"
	"github.com/sigstore/timestamp-authority/pkg/log"
)

//go:generate swagger generate server --target ../../generated --name TimestampServer --spec ../../../openapi.yaml --principal interface{} --exclude-main --exclude-spec

func configureFlags(api *operations.TimestampServerAPI) {
	// api.CommandLineOptionsGroups = []swag.CommandLineOptionsGroup{ ... }
}

func configureAPI(api *operations.TimestampServerAPI) http.Handler {
	// configure the api here
	api.ServeError = logAndServeError

	// Set your custom logger if needed. Default one is log.Printf
	// Expected interface func(string, ...interface{})
	//
	// Example:
	// api.Logger = log.Printf
	api.Logger = log.Logger.Infof

	// api.UseSwaggerUI()
	// To continue using redoc as your UI, uncomment the following line
	// api.UseRedoc()

	api.JSONConsumer = runtime.JSONConsumer()
	api.JSONProducer = runtime.JSONProducer()

	api.ApplicationPemCertificateChainProducer = runtime.TextProducer()
	api.ApplicationTimestampQueryConsumer = runtime.ByteStreamConsumer()
	api.ApplicationTimestampReplyProducer = runtime.ByteStreamProducer()

	api.TimestampGetTimestampResponseHandler = timestamp.GetTimestampResponseHandlerFunc(pkgapi.TimestampResponseHandler)
	api.TimestampGetTimestampCertChainHandler = timestamp.GetTimestampCertChainHandlerFunc(pkgapi.GetTimestampCertChainHandler)

	api.PreServerShutdown = func() {}

	api.ServerShutdown = func() {}

	api.AddMiddlewareFor("POST", "/api/v1/timestamp", middleware.NoCache)
	api.AddMiddlewareFor("GET", "/api/v1/timestamp/certchain", cacheForDay)

	return setupGlobalMiddleware(api.Serve(setupMiddlewares))
}

// The TLS configuration before HTTPS server starts.
func configureTLS(tlsConfig *tls.Config) {
	// Make all necessary changes to the TLS configuration here.
}

// As soon as server is initialized but not run yet, this function will be called.
// If you need to modify a config, store server instance to stop it individually later, this is the place.
// This function can be called multiple times, depending on the number of serving schemes.
// scheme value will be set accordingly: "http", "https" or "unix".
func configureServer(s *http.Server, scheme, addr string) {
}

// The middleware configuration is for the handler executors. These do not apply to the swagger.json document.
// The middleware executes after routing but before authentication, binding and validation.
func setupMiddlewares(handler http.Handler) http.Handler {
	return handler
}

// We need this type to act as an adapter between zap and the middleware request logger.
type logAdapter struct {
}

func (l *logAdapter) Print(v ...interface{}) {
	log.Logger.Info(v...)
}

// The middleware configuration happens before anything, this middleware also applies to serving the swagger.json document.
// So this is a good place to plug in a panic handling middleware, logging and metrics.
func setupGlobalMiddleware(handler http.Handler) http.Handler {
	middleware.DefaultLogger = middleware.RequestLogger(
		&middleware.DefaultLogFormatter{Logger: &logAdapter{}})
	returnHandler := middleware.Logger(handler)
	returnHandler = middleware.Recoverer(returnHandler)
	returnHandler = middleware.Heartbeat("/ping")(returnHandler)

	handleCORS := cors.Default().Handler
	returnHandler = handleCORS(returnHandler)

	returnHandler = wrapMetrics(returnHandler)

	return middleware.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r = r.WithContext(log.WithRequestID(ctx, middleware.GetReqID(ctx)))
		defer func() {
			_ = log.RequestIDLogger(r).Sync()
		}()

		returnHandler.ServeHTTP(w, r)
	}))
}

func wrapMetrics(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		defer func() {
			// This logs latency broken down by URL path and response code
			pkgapi.MetricLatency.With(map[string]string{
				"path": r.URL.Path,
				"code": strconv.Itoa(ww.Status()),
			}).Observe(float64(time.Since(start)))
		}()

		handler.ServeHTTP(ww, r)

	})
}

func cacheForDay(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := negroni.NewResponseWriter(w)
		ww.Before(func(w negroni.ResponseWriter) {
			if w.Status() >= 200 && w.Status() <= 299 {
				w.Header().Set("Cache-Control", "max-age=86400, immutable")
			}
		})
		handler.ServeHTTP(ww, r)
	})
}

func logAndServeError(w http.ResponseWriter, r *http.Request, err error) {
	if apiErr, ok := err.(errors.Error); ok && apiErr.Code() == http.StatusNotFound {
		log.RequestIDLogger(r).Warn(err)
	} else {
		log.RequestIDLogger(r).Error(err)
	}
	requestFields := map[string]interface{}{}
	if err := mapstructure.Decode(r, &requestFields); err == nil {
		log.RequestIDLogger(r).Debug(requestFields)
	}
	errors.ServeError(w, r, err)
}
