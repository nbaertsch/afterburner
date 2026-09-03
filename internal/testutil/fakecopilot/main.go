package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
)

type capture struct {
	Args       []string          `json:"args"`
	Env        map[string]string `json:"env"`
	ProxyReady bool              `json:"proxyReady,omitempty"`
}

func main() {
	path := os.Getenv("AFTERBURNER_TEST_CAPTURE")
	value := capture{
		Args: append([]string(nil), os.Args[1:]...),
		Env: map[string]string{
			"COPILOT_HOME":                    os.Getenv("COPILOT_HOME"),
			"AFTERBURNER_HOME":                os.Getenv("AFTERBURNER_HOME"),
			"AFTERBURNER_BASE_PACKAGE":        os.Getenv("AFTERBURNER_BASE_PACKAGE"),
			"AFTERBURNER_BASE_APP_SHA256":     os.Getenv("AFTERBURNER_BASE_APP_SHA256"),
			"AFTERBURNER_BASE_RUNTIME_SHA256": os.Getenv("AFTERBURNER_BASE_RUNTIME_SHA256"),
		},
	}
	if proxyURL := os.Getenv("AFTERBURNER_TEST_PROXY_URL"); proxyURL != "" {
		response, err := http.Get(proxyURL + "/__afterburner/byomodels/health")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(41)
		}
		value.ProxyReady = response.StatusCode == http.StatusOK
		_ = response.Body.Close()
		if !value.ProxyReady {
			fmt.Fprintf(os.Stderr, "proxy health status = %d\n", response.StatusCode)
			os.Exit(42)
		}
	}
	data, _ := json.Marshal(value)
	_ = os.WriteFile(path, data, 0o600)
	if os.Getenv("AFTERBURNER_TEST_WAIT_FOR_INTERRUPT") == "1" {
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		fmt.Println("fake-copilot-ready")
		<-interrupts
		fmt.Println("fake-copilot-interrupted")
		os.Exit(130)
	}
	if raw := os.Getenv("AFTERBURNER_TEST_EXIT_CODE"); raw != "" {
		code, _ := strconv.Atoi(raw)
		os.Exit(code)
	}
}
