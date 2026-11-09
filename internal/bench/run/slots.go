package run

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/iml885203/lodog/internal/casecfg"
)

// slotsNeeded is how many slots the run uses: one unless a shareable task is
// planned.
func (p Plan) slotsNeeded() int {
	if shared, _ := p.split(); len(shared) > 0 {
		return p.slots()
	}
	return 1
}

// slotConfigs gives every slot a gateway config of its own. Slot 0 serves at
// the config's addresses; every other slot at free ports on the same hosts,
// so each agent's MCP config reaches only its own attempt's gateway, and the
// calls that gateway labels are that attempt's.
func slotConfigs(ctx context.Context, cfg *casecfg.Config, slots int) ([]*casecfg.Config, error) {
	configs := []*casecfg.Config{cfg}
	for range slots - 1 {
		c := *cfg
		listen, err := freeAddr(ctx, cfg.Listen)
		if err != nil {
			return nil, err
		}
		c.Listen = listen
		if cfg.Approvals.Listen != "" {
			if c.Approvals.Listen, err = freeAddr(ctx, cfg.Approvals.Listen); err != nil {
				return nil, err
			}
		}
		configs = append(configs, &c)
	}
	return configs, nil
}

// freeAddr is a port nothing listens on, on the host of addr.
func freeAddr(ctx context.Context, addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("%w: listen address %q: %w", ErrPreflight, addr, err)
	}
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return "", fmt.Errorf("%w: find a free port on %s: %w", ErrPreflight, host, err)
	}
	free := lis.Addr().String()
	if err := lis.Close(); err != nil {
		return "", fmt.Errorf("release port %s: %w", free, err)
	}
	return free, nil
}

func mcpConfigName(slot int) string {
	if slot == 0 {
		return "mcp.json"
	}
	return "mcp-" + strconv.Itoa(slot) + ".json"
}
