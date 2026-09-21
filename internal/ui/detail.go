package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jonaskpb/docker-registry-manager/internal/registry"
)

// renderDetail builds the scrollable detail page for one tag: a summary, the
// platform/layer breakdown, the image config, and finally the raw manifest.
func renderDetail(host, repo, tag string, m *registry.Manifest, cfg *registry.ImageConfig) string {
	var b strings.Builder

	section := func(name string) {
		b.WriteString("\n")
		b.WriteString(styleTitle.Render("  " + name))
		b.WriteString("\n")
	}
	field := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString("  " + styleLabel.Render(pad(k, 14)) + " " + styleValue.Render(v) + "\n")
	}

	b.WriteString(styleTitle.Render("  IMAGE"))
	b.WriteString("\n")
	field("Reference", host+"/"+repo+":"+tag)
	field("Digest", m.Digest)
	field("Pull by digest", host+"/"+repo+"@"+m.Digest)
	field("Media type", m.MediaType)
	field("Schema", fmt.Sprintf("v%d", m.SchemaVersion))
	field("Total size", fmt.Sprintf("%s (%d bytes)", humanSize(m.Size()), m.Size()))

	if cfg != nil {
		if cfg.Created != nil && !cfg.Created.IsZero() {
			field("Created", fmt.Sprintf("%s  (%s ago)", cfg.Created.Local().Format(time.RFC3339), humanAge(*cfg.Created)))
		}
		if p := cfg.Platform(); p != nil {
			field("Platform", p.String())
		}
		field("Author", cfg.Author)
	}

	if m.IsIndex() {
		section(fmt.Sprintf("PLATFORMS (%d)", len(m.Manifests)))
		b.WriteString("  " + styleColumn.Render(pad("PLATFORM", 22)+" "+pad("DIGEST", 14)+" "+pad("SIZE", 12)+" MEDIA TYPE") + "\n")
		for _, d := range m.Manifests {
			platform := d.Platform.String()
			if platform == "" || platform == "/" {
				platform = "-"
			}
			line := fmt.Sprintf("  %s %s %s %s",
				pad(platform, 22),
				pad(registry.ShortDigest(d.Digest), 14),
				pad(humanSize(d.Size), 12),
				d.MediaType)
			if d.Platform != nil && d.Platform.OS == "unknown" {
				// buildkit attestation manifests, not pullable images
				b.WriteString(styleRowFaint.Render(line) + "\n")
				continue
			}
			b.WriteString(styleRow.Render(line) + "\n")
		}
	} else {
		section(fmt.Sprintf("LAYERS (%d)", len(m.Layers)))
		b.WriteString("  " + styleColumn.Render(pad("#", 4)+" "+pad("DIGEST", 14)+" "+pad("SIZE", 12)+" MEDIA TYPE") + "\n")
		for i, l := range m.Layers {
			b.WriteString(styleRow.Render(fmt.Sprintf("  %s %s %s %s",
				pad(fmt.Sprint(i+1), 4),
				pad(registry.ShortDigest(l.Digest), 14),
				pad(humanSize(l.Size), 12),
				l.MediaType)) + "\n")
		}
		if m.Config.Digest != "" {
			b.WriteString(styleRowFaint.Render(fmt.Sprintf("  %s %s %s %s",
				pad("cfg", 4),
				pad(registry.ShortDigest(m.Config.Digest), 14),
				pad(humanSize(m.Config.Size), 12),
				m.Config.MediaType)) + "\n")
		}
	}

	if cfg != nil {
		section("CONFIG")
		field("User", cfg.Config.User)
		field("WorkingDir", cfg.Config.WorkingDir)
		field("Entrypoint", strings.Join(cfg.Config.Entrypoint, " "))
		field("Cmd", strings.Join(cfg.Config.Cmd, " "))
		if len(cfg.Config.ExposedPorts) > 0 {
			ports := make([]string, 0, len(cfg.Config.ExposedPorts))
			for p := range cfg.Config.ExposedPorts {
				ports = append(ports, p)
			}
			field("Exposed", strings.Join(ports, " "))
		}
		if len(cfg.Config.Env) > 0 {
			b.WriteString("  " + styleLabel.Render(pad("Env", 14)) + "\n")
			for _, e := range cfg.Config.Env {
				b.WriteString("    " + styleValue.Render(e) + "\n")
			}
		}
		if len(cfg.Config.Labels) > 0 {
			b.WriteString("  " + styleLabel.Render(pad("Labels", 14)) + "\n")
			for k, v := range cfg.Config.Labels {
				b.WriteString("    " + styleValue.Render(k+"="+v) + "\n")
			}
		}

		if len(cfg.History) > 0 {
			section(fmt.Sprintf("HISTORY (%d)", len(cfg.History)))
			for _, h := range cfg.History {
				stamp := "-"
				if h.Created != nil {
					stamp = h.Created.Local().Format("2006-01-02 15:04")
				}
				cmd := strings.ReplaceAll(h.CreatedBy, "\t", " ")
				cmd = strings.Join(strings.Fields(cmd), " ")
				line := fmt.Sprintf("  %s  %s", pad(stamp, 16), cmd)
				if h.EmptyLayer {
					b.WriteString(styleRowFaint.Render(line) + "\n")
				} else {
					b.WriteString(styleRow.Render(line) + "\n")
				}
			}
		}
	}

	if len(m.Annotations) > 0 {
		section("ANNOTATIONS")
		for k, v := range m.Annotations {
			field(k, v)
		}
	}

	section("MANIFEST")
	for _, line := range strings.Split(prettyJSON(m.Raw), "\n") {
		b.WriteString(styleRowFaint.Render("  "+line) + "\n")
	}

	return b.String()
}
