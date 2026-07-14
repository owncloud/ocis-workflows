package command

import (
	"fmt"
	"net/http"

	"github.com/LukasHirt/ocis-workflows/pkg/config"
)

// RunHealth checks the debug server's /healthz endpoint and returns an error if it's not
// reporting healthy — used by `workflows health` for container healthchecks.
func RunHealth(cfg config.Config) error {
	res, err := http.Get("http://" + cfg.DebugAddr + "/healthz") //nolint:noctx // one-shot CLI check
	if err != nil {
		return fmt.Errorf("health check request failed: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("health check returned status %d", res.StatusCode)
	}
	return nil
}
