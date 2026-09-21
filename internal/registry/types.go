package registry

import (
	"fmt"
	"strings"
	"time"
)

// Media types defined by the OCI image spec and the Docker image manifest v2
// spec. A registry may hand back any of these, so every manifest request
// advertises all of them in its Accept header.
const (
	MediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	MediaTypeDockerManifestV1   = "application/vnd.docker.distribution.manifest.v1+json"
	MediaTypeDockerManifestV1JS = "application/vnd.docker.distribution.manifest.v1+prettyjws"
	MediaTypeOCIManifest        = "application/vnd.oci.image.manifest.v1+json"
	MediaTypeOCIIndex           = "application/vnd.oci.image.index.v1+json"
	MediaTypeDockerConfig       = "application/vnd.docker.container.image.v1+json"
	MediaTypeOCIConfig          = "application/vnd.oci.image.config.v1+json"
)

// manifestAccept is the Accept header sent with every manifest request. Order
// expresses preference: index types first so multi-arch images resolve to the
// index rather than to whatever single platform the registry would pick.
var manifestAccept = strings.Join([]string{
	MediaTypeOCIIndex,
	MediaTypeDockerManifestList,
	MediaTypeOCIManifest,
	MediaTypeDockerManifest,
	MediaTypeDockerManifestV1,
	MediaTypeDockerManifestV1JS,
	"*/*",
}, ", ")

// Descriptor points at a blob or a nested manifest.
type Descriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

// Platform identifies the target of one entry in a manifest list / index.
type Platform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	OSVersion    string `json:"os.version,omitempty"`
	Variant      string `json:"variant,omitempty"`
}

// String renders a platform the way the Docker CLI does: os/arch[/variant].
func (p *Platform) String() string {
	if p == nil {
		return ""
	}
	s := p.OS + "/" + p.Architecture
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// Manifest is the union of an image manifest and a manifest list / index. Only
// one of Layers or Manifests is populated, depending on MediaType.
type Manifest struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	Config        Descriptor        `json:"config"`
	Layers        []Descriptor      `json:"layers"`
	Manifests     []Descriptor      `json:"manifests"`
	Annotations   map[string]string `json:"annotations,omitempty"`

	// Digest is not part of the wire format; it is filled in from the
	// Docker-Content-Digest response header.
	Digest string `json:"-"`
	// Raw keeps the exact bytes the registry returned so the detail view can
	// show the manifest verbatim.
	Raw []byte `json:"-"`
}

// IsIndex reports whether the manifest is a multi-platform list / index.
func (m *Manifest) IsIndex() bool {
	return m.MediaType == MediaTypeOCIIndex || m.MediaType == MediaTypeDockerManifestList || len(m.Manifests) > 0
}

// Size is the number of bytes this manifest accounts for: the config blob plus
// every layer.
//
// For an index the descriptors point at child manifest *documents*, not at
// image content, so the sum here is only a few kilobytes and says nothing
// about how much storage the image uses. Client.DescribeTag resolves the real
// figure by following the children; prefer TagInfo.Size for anything a user
// reads.
func (m *Manifest) Size() int64 {
	var total int64
	if m.IsIndex() {
		for _, d := range m.Manifests {
			total += d.Size
		}
		return total
	}
	total = m.Config.Size
	for _, l := range m.Layers {
		total += l.Size
	}
	return total
}

// ImageConfig is the subset of the image config blob the UI displays.
type ImageConfig struct {
	Created      *time.Time `json:"created"`
	Architecture string     `json:"architecture"`
	OS           string     `json:"os"`
	Variant      string     `json:"variant,omitempty"`
	Author       string     `json:"author,omitempty"`
	Config       struct {
		Entrypoint   []string            `json:"Entrypoint"`
		Cmd          []string            `json:"Cmd"`
		Env          []string            `json:"Env"`
		Labels       map[string]string   `json:"Labels"`
		WorkingDir   string              `json:"WorkingDir"`
		User         string              `json:"User"`
		ExposedPorts map[string]struct{} `json:"ExposedPorts"`
	} `json:"config"`
	History []struct {
		Created    *time.Time `json:"created"`
		CreatedBy  string     `json:"created_by"`
		EmptyLayer bool       `json:"empty_layer"`
		Comment    string     `json:"comment"`
	} `json:"history"`
}

// Platform returns the config's platform, or nil when it declares none.
func (c *ImageConfig) Platform() *Platform {
	if c == nil || (c.OS == "" && c.Architecture == "") {
		return nil
	}
	return &Platform{OS: c.OS, Architecture: c.Architecture, Variant: c.Variant}
}

// Error is a registry API error response (a non-2xx with a JSON body).
type Error struct {
	StatusCode int
	Method     string
	URL        string
	Errors     []APIError
	Body       string
}

// APIError is one entry of the registry's errors array.
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, a := range e.Errors {
			if a.Message != "" {
				parts = append(parts, fmt.Sprintf("%s: %s", a.Code, a.Message))
			} else {
				parts = append(parts, a.Code)
			}
		}
		return fmt.Sprintf("%s (HTTP %d)", strings.Join(parts, "; "), e.StatusCode)
	}
	if body := strings.TrimSpace(e.Body); body != "" && len(body) < 200 {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, body)
	}
	return fmt.Sprintf("HTTP %d from %s %s", e.StatusCode, e.Method, e.URL)
}

// Code reports whether the error carries the given registry error code, e.g.
// UNSUPPORTED when deletes are disabled or MANIFEST_UNKNOWN for a missing tag.
func (e *Error) Code(code string) bool {
	for _, a := range e.Errors {
		if strings.EqualFold(a.Code, code) {
			return true
		}
	}
	return false
}
