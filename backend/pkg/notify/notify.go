// Package notify sends workflow notifications via github.com/unraid/apprise-go, a pure-Go
// port of Apprise giving 100+ notification integrations (Slack, Discord, email, webhooks,
// ...) behind a single target-URL scheme. BSD-2-Clause, see LICENSES/BSD-2-Clause.txt.
package notify

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"

	apprise "github.com/unraid/apprise-go"
)

// Send delivers a notification to the given apprise target URL. Target URLs are treated as
// secrets by callers (they often embed webhook tokens) — this package only ever logs
// success/failure, never the target or body.
func Send(ctx context.Context, target, title, body string) error {
	if err := guardTarget(target); err != nil {
		return err
	}

	client := apprise.New()
	if err := client.Add(target); err != nil {
		return fmt.Errorf("invalid notification target: %w", err)
	}

	return client.Send(body, apprise.WithTitle(title))
}

// guardTarget blocks generic webhook-style targets (json://, form://, ...) that resolve to
// loopback/private/link-local addresses, so a workflow can't use the notify action to probe
// the backend's own network. Provider-specific schemes (slack://, discord://, mailto://,
// ...) are not raw URLs to arbitrary hosts and are allowed through unchanged.
func guardTarget(target string) error {
	u, err := url.Parse(target)
	if err != nil {
		return fmt.Errorf("invalid notification target: %w", err)
	}

	switch strings.ToLower(u.Scheme) {
	case "json", "jsons", "form", "forms", "xml", "xmls", "http", "https":
	default:
		return nil
	}

	host := u.Hostname()
	if host == "" {
		return nil
	}

	ips, err := net.DefaultResolver.LookupIPAddr(context.Background(), host)
	if err != nil {
		// Can't resolve it up front — let apprise-go's own request fail naturally rather
		// than blocking on a resolution error here.
		return nil
	}
	for _, ip := range ips {
		if isDisallowedTarget(ip.IP) {
			return fmt.Errorf("notification target host %q resolves to a disallowed address", host)
		}
	}
	return nil
}

func isDisallowedTarget(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
