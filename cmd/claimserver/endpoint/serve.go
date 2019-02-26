package endpoint

import (
	"context"
	"net/http"
	"os"
	"os/signal"

	"github.com/gin-contrib/cors"

	"github.com/gin-gonic/gin"
	"github.com/iden3/go-iden3/cmd/claimserver/config"
	"github.com/iden3/go-iden3/services/adminsrv"
	"github.com/iden3/go-iden3/services/claimsrv"
	"github.com/iden3/go-iden3/services/rootsrv"

	log "github.com/sirupsen/logrus"
)

var claimservice claimsrv.Service
var rootservice rootsrv.Service
var adminservice adminsrv.Service

func init() {
	gin.SetMode(gin.ReleaseMode)
}

func serveServiceApi() *http.Server {
	// start serviceapi
	api := gin.Default()
	api.Use(cors.Default())

	serviceapi := api.Group("/api/unstable")
	serviceapi.GET("/root", handleGetRoot)

	serviceapi.POST("/claims", handlePostClaim)                  // Get relay claim proof
	serviceapi.GET("/claims/:hi/proof", handleGetClaimProofByHi) // Get relay claim proof

	serviceapisrv := &http.Server{Addr: config.C.Server.ServiceApi, Handler: api}
	go func() {
		log.Info("API server at ", config.C.Server.ServiceApi)
		if err := serviceapisrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("listen: %s\n", err)
		}
	}()
	return serviceapisrv
}

func serveAdminApi(stopch chan interface{}) *http.Server {
	api := gin.Default()
	api.Use(cors.Default())
	adminapi := api.Group("/api/unstable")

	adminapi.POST("/stop", func(c *gin.Context) {
		// yeah, use curl -X POST http://<adminserver>/stop
		c.String(http.StatusOK, "got it, shutdowning server")
		stopch <- nil
	})

	adminapi.GET("/info", handleInfo)
	adminapi.GET("/rawdump", handleRawDump)
	adminapi.POST("/rawimport", handleRawImport)
	adminapi.GET("/claimsdump", handleClaimsDump)
	adminapi.POST("/claims/basic", handleAddClaimBasic)

	adminapisrv := &http.Server{Addr: config.C.Server.AdminApi, Handler: api}
	go func() {
		log.Info("ADMIN server at ", config.C.Server.AdminApi)
		if err := adminapisrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Errorf("listen: %s\n", err)
		}
	}()
	return adminapisrv
}

func Serve(rs rootsrv.Service, cs claimsrv.Service, as adminsrv.Service) {

	claimservice = cs
	rootservice = rs
	adminservice = as

	stopch := make(chan interface{})

	// catch ^C to send the stop signal
	ossig := make(chan os.Signal, 1)
	signal.Notify(ossig, os.Interrupt)
	go func() {
		for sig := range ossig {
			if sig == os.Interrupt {
				stopch <- nil
			}
		}
	}()

	// start servers
	rootservice.Start()
	serviceapisrv := serveServiceApi()
	adminapisrv := serveAdminApi(stopch)

	// wait until shutdown signal
	<-stopch
	log.Info("Shutdown Server ...")

	if err := serviceapisrv.Shutdown(context.Background()); err != nil {
		log.Error("ServiceApi Shutdown:", err)
	} else {
		log.Info("ServiceApi stopped")
	}

	if err := adminapisrv.Shutdown(context.Background()); err != nil {
		log.Error("AdminApi Shutdown:", err)
	} else {
		log.Info("AdminApi stopped")
	}

}