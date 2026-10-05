package deploy

import (
	"context"
	"encoding/base64"
	"fmt"
	"path"
	"strings"
)

// The Caddy wiring deploy owns:
//
//	/etc/caddy/Caddyfile          one import line, added once
//	/etc/caddy/mymo/<app>.caddy   one route file per public app
//
// The route file targets the release's container name — its unique
// DNS identity on the network — so a reload switches releases
// atomically and no window ever mixes two. Every new route is
// validated before the reload that activates it; when validation
// fails the previous content is restored and the reload never runs,
// so the old route keeps serving.
const (
	caddyFile     = "/etc/caddy/Caddyfile"
	caddyRouteDir = "/etc/caddy/mymo"
	importLine    = "import mymo/*.caddy"
)

// renderRoute draws the one site block mymo owns for an app. The
// comment is part of the contract: mymo overwrites what it owns.
func renderRoute(name, domainName, container string, port int) string {
	return fmt.Sprintf("# mymo route for %s — managed by mymo; edits are overwritten\n%s {\n\treverse_proxy %s:%d\n}\n",
		name, domainName, container, port)
}

// activateRoute makes caddy serve container for the app's domain:
// the route file is written (guarded, backed up beside itself), the
// whole config is validated, and only then does the reload run. On
// any refusal the previous content is restored and returned as an
// error — the caller stops the new release, the old route keeps
// serving.
func activateRoute(ctx context.Context, r Runner, o Options, app domainApp, container string) (string, error) {
	routePath := path.Join(caddyRouteDir, app.Name+".caddy")
	content := renderRoute(app.Name, app.Domain, container, app.Port)

	if err := ensureImport(ctx, r, o); err != nil {
		return "", err
	}

	kept, old, err := guardedWrite(ctx, r, o, routePath, content, "0644", "root:root")
	if err != nil {
		return "", err
	}
	if kept {
		return "the route already pointed at " + container, nil
	}
	if out, code, err := runW(ctx, r, o, "caddy", "validate", "--config", caddyFile); err != nil || code != 0 {
		// restore before refusing: the file on the node must be the
		// one that was serving
		if rErr := restore(ctx, r, o, routePath, old); rErr != nil {
			return "", fmt.Errorf("the new route did not validate (%s) and the old one could not be restored: %v", firstLine(out), rErr)
		}
		return "", fmt.Errorf("the new route did not validate: %s", firstLine(out))
	}
	if out, code, err := runW(ctx, r, o, "systemctl", "reload", "caddy"); err != nil || code != 0 {
		if rErr := restore(ctx, r, o, routePath, old); rErr == nil {
			// the reload may have half-happened; restoring the file
			// and reloading again is the honest recovery
			runW(ctx, r, o, "systemctl", "reload", "caddy")
		}
		return "", fmt.Errorf("the caddy reload failed (%d): %s", code, firstLine(out))
	}
	return app.Domain + " → " + container + " — route swapped and reloaded", nil
}

// ensureImport wires mymo's route directory into the Caddyfile
// exactly once, with a backup of the original beside it.
func ensureImport(ctx context.Context, r Runner, o Options) error {
	// the exit code — not the output — says the Caddyfile is there:
	// a failed cat folds its error into the stream, and writing that
	// back would put cat's complaint inside the serving config
	current, catCode, _ := runW(ctx, r, o, "cat", caddyFile)
	if catCode != 0 {
		current = ""
	}
	if strings.Contains(current, importLine) {
		return nil
	}
	if _, _, err := runW(ctx, r, o, "mkdir", "-p", caddyRouteDir); err != nil {
		return err
	}
	next := importLine + "\n" + current
	if _, _, err := guardedWrite(ctx, r, o, caddyFile, next, "0644", "root:root"); err != nil {
		return fmt.Errorf("could not wire %s into the Caddyfile: %v", caddyRouteDir, err)
	}
	o.report("wired · " + caddyFile + " now imports " + caddyRouteDir)
	return nil
}

// guardedWrite writes content to path with the same discipline as
// the apply engine: identical content is kept, a differing file is
// backed up beside itself once (the original surviving every
// re-run), and the write lands through a tmp file moved into place.
// It returns whether the content was already in place and what the
// previous content was, for restore on validation failure.
func guardedWrite(ctx context.Context, r Runner, o Options, file, content, mode, owner string) (kept bool, old string, err error) {
	current, code, _ := runW(ctx, r, o, "cat", file)
	if code == 0 && current == content {
		return true, current, nil
	}
	if code == 0 && current != "" {
		if _, bakCode, _ := runW(ctx, r, o, "test", "-f", file+".mymo-bak"); bakCode != 0 {
			if _, cCode, cErr := runW(ctx, r, o, "cp", file, file+".mymo-bak"); cErr != nil || cCode != 0 {
				return false, "", fmt.Errorf("could not back up %s", file)
			}
			o.report("backed up · " + file + " → " + file + ".mymo-bak")
		}
	}
	if out, cCode, cErr := runW(ctx, r, o, "mkdir", "-p", path.Dir(file)); cErr != nil || cCode != 0 {
		return false, "", fmt.Errorf("could not create the parent of %s: %s", file, firstLine(out))
	}
	tmp := file + ".mymo-new"
	script := fmt.Sprintf("printf %%s %s | base64 -d > %s && chmod %s %s && chown %s %s && mv %s %s",
		base64.StdEncoding.EncodeToString([]byte(content)), tmp, mode, tmp, owner, tmp, tmp, file)
	if out, cCode, cErr := runW(ctx, r, o, "sh", "-c", script); cErr != nil || cCode != 0 {
		return false, "", fmt.Errorf("writing %s failed (%d): %s", file, cCode, firstLine(out))
	}
	return false, current, nil
}

// restore puts content back to file after a validation refused the
// new one — the serving config must be the one that was serving.
func restore(ctx context.Context, r Runner, o Options, file, content string) error {
	if content == "" {
		// there was no route before; the refusal must remove the
		// new one rather than write an empty file
		_, _, err := runW(ctx, r, o, "rm", "-f", file)
		return err
	}
	_, _, err := guardedWrite(ctx, r, o, file, content, "0644", "root:root")
	return err
}

// domainApp is the slice of domain.App the caddy wiring needs.
type domainApp struct {
	Name   string
	Domain string
	Port   int
}
