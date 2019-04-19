package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/asaskevich/govalidator"
	"github.com/brave-intl/bat-go/controllers"
	"github.com/brave-intl/bat-go/grant"
	"github.com/brave-intl/bat-go/middleware"
	"github.com/garyburd/redigo/redis"
	raven "github.com/getsentry/raven-go"
	"github.com/go-chi/chi"
	chiware "github.com/go-chi/chi/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/hlog"
	"github.com/rs/zerolog/log"
)

var (
	redisURL = os.Getenv("REDIS_URL")
)

func setupLogger(ctx context.Context) (context.Context, *zerolog.Logger) {
	// set time field to unix
	zerolog.TimeFieldFormat = time.UnixDate
	// always print out timestamp
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	return log.Logger.WithContext(ctx), &log.Logger
}

func setupRouter(ctx context.Context, logger *zerolog.Logger) (context.Context, *chi.Mux) {
	govalidator.SetFieldsRequiredByDefault(true)

	r := chi.NewRouter()
	r.Use(chiware.RequestID)

	// NOTE: This uses standard fowarding headers, note that this puts implicit trust in the header values
	// provided to us. In particular it uses the first element.
	// (e.g. with header "X-Forwarded-For: client, proxy1, proxy2" it would yield "client" as the real IP.)
	// The grant server is only accessed by the ledger service, so headers are semi-trusted.
	// Consequently we should consider the request IP as primarily "informational".
	r.Use(chiware.RealIP)

	r.Use(chiware.Heartbeat("/"))
	r.Use(chiware.Timeout(60 * time.Second))
	r.Use(middleware.BearerToken)
	r.Use(hlog.NewHandler(*logger))
	if logger != nil {
		// Also handles panic recovery
		r.Use(hlog.RemoteAddrHandler("ip"))
		r.Use(hlog.UserAgentHandler("user_agent"))
		r.Use(hlog.RefererHandler("referer"))
		r.Use(hlog.RequestIDHandler("req_id", "Request-Id"))
		r.Use(middleware.RequestLogger(logger))
	}

	redisAddress := "localhost:6379"
	if len(redisURL) > 0 {
		redisAddress = strings.TrimPrefix(redisURL, "redis://")
	}
	rp := &redis.Pool{
		MaxIdle:     3,
		IdleTimeout: 240 * time.Second,
		Dial:        func() (redis.Conn, error) { return redis.Dial("tcp", redisAddress) },
	}

	service, err := grant.InitService(&grant.Redis{Pool: rp}, rp)
	if err != nil {
		raven.CaptureErrorAndWait(err, nil)
		log.Panic().Err(err)
	}

	r.Mount("/v1/grants", controllers.GrantsRouter(service))
	r.Get("/metrics", middleware.Metrics())
	return ctx, r
}

func main() {
	serverCtx, _ := setupLogger(context.Background())
	logger := log.Ctx(serverCtx)
	subLog := logger.Info().Str("prefix", "main")
	subLog.Msg("Starting server")

	serverCtx, r := setupRouter(serverCtx, logger)

	srv := http.Server{Addr: ":3333", Handler: chi.ServerBaseContext(serverCtx, r)}
	err := srv.ListenAndServe()
	if err != nil {
		raven.CaptureErrorAndWait(err, nil)
		logger.Panic().Err(err)
	}
}
