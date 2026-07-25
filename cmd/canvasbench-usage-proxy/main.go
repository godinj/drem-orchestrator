package main

import (
	"flag"
	"log"
	"net/http"
	"time"

	"github.com/godinj/drem-orchestrator/internal/benchv2"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:18091", "usage proxy listen address")
	publicBaseURL := flag.String("public-base-url", "", "outer-harness-reachable /v1 base URL")
	upstream := flag.String("upstream", "", "trusted upstream /v1/chat/completions URL")
	adminTokenFile := flag.String("admin-token-file", "", "owner-only admin token file")
	upstreamAPIKeyFile := flag.String("upstream-api-key-file", "", "optional owner-only upstream API key file")
	sourceState := flag.String("source-state", "", "attested source state for this exact proxy build")
	image := flag.String("image", "", "digest-pinned OCI identity for this exact proxy build")
	configSHA256 := flag.String("config-sha256", "", "SHA-256 of the non-secret effective proxy configuration")
	flag.Parse()
	adminToken, err := benchv2.ReadPrivateTokenFile(*adminTokenFile)
	if err != nil {
		log.Fatal(err)
	}
	upstreamAPIKey := ""
	if *upstreamAPIKeyFile != "" {
		upstreamAPIKey, err = benchv2.ReadPrivateTokenFile(*upstreamAPIKeyFile)
		if err != nil {
			log.Fatal(err)
		}
	}
	handler, err := benchv2.NewUsageProxyHandler(benchv2.UsageProxyServerConfig{
		UpstreamChatCompletions: *upstream, UpstreamAPIKey: upstreamAPIKey,
		PublicBaseURL: *publicBaseURL, AdminToken: adminToken,
		SourceState: *sourceState, Image: *image, ConfigSHA256: *configSHA256,
	})
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr: *listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout: 30 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 30 * time.Second,
	}
	log.Printf("canvasbench usage proxy listening on %s", *listen)
	log.Fatal(server.ListenAndServe())
}
