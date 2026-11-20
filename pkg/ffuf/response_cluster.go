package ffuf

import (
	"bytes"
	"math/bits"
	"net/url"
	"sort"
	"strings"
	"sync"
)

const (
	// DefaultResponseClusterThreshold is the minimum similarity at which two
	// responses are considered members of the same cluster.
	DefaultResponseClusterThreshold = 0.85
	// DefaultResponseClusterMaxBody bounds the body bytes used for a
	// fingerprint. Large bodies still cluster, but trailing content is ignored.
	DefaultResponseClusterMaxBody = 1 << 20
	// DefaultResponseClusterMaxClusters bounds the fingerprint memory retained
	// per scope. Once the limit is reached, new responses pass unfiltered rather
	// than being silently merged into an unrelated cluster.
	DefaultResponseClusterMaxClusters = 10000

	clusterShingleSize  = 8
	clusterHashPrime    = uint64(1099511628211)
	clusterMinMaskValue = 3
	clusterMaxShingles  = 1 << 16
)

// ClusterOptions controls response similarity clustering.
type ClusterOptions struct {
	// Threshold is the minimum SimHash similarity in (0, 1].
	Threshold float64
	// MaxBodyBytes is the maximum response body prefix used for similarity.
	MaxBodyBytes int
	// MaxClusters is the per-host cluster limit.
	MaxClusters int
	// AutoCalibrationOnly suppresses only clusters learned from autocalibration
	// probes. Ordinary duplicate response shapes are still reported, matching the
	// behavior of a plain -ac scan.
	AutoCalibrationOnly bool

	deduplicate bool
}

// DefaultClusterOptions returns the defaults used by -rc.
func DefaultClusterOptions() ClusterOptions {
	return ClusterOptions{
		Threshold:    DefaultResponseClusterThreshold,
		MaxBodyBytes: DefaultResponseClusterMaxBody,
		MaxClusters:  DefaultResponseClusterMaxClusters,
	}
}

func (o ClusterOptions) normalized() ClusterOptions {
	if o.Threshold <= 0 || o.Threshold > 1 {
		o.Threshold = DefaultResponseClusterThreshold
	}
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = DefaultResponseClusterMaxBody
	}
	if o.MaxClusters <= 0 {
		o.MaxClusters = DefaultResponseClusterMaxClusters
	}
	o.deduplicate = !o.AutoCalibrationOnly
	return o
}

// ClusterDecision describes what happened when a response was presented to the
// clusterer.
type ClusterDecision struct {
	ClusterID int
	Size      int
	// Duplicate is true when the response joined an existing cluster. In -rc
	// mode the first response in a cluster is the representative; later members
	// are suppressed.
	Duplicate bool
	// Calibrated is true when the response belongs to a cluster learned from
	// autocalibration probes. Calibrated clusters are always suppressed.
	Calibrated bool
	// Overflow is true when the per-host cluster limit prevented tracking. Such
	// responses are never suppressed.
	Overflow bool
	dedupe   bool
}

// Keep reports whether the response should be emitted.
func (d ClusterDecision) Keep() bool {
	return d.Overflow || (!d.Calibrated && !(d.Duplicate && d.dedupe))
}

type responseFingerprint struct {
	status      int64
	contentType string
	exact       uint64
	simhash     uint64
	short       bool
	empty       bool
}

type responseCluster struct {
	id          int
	fingerprint responseFingerprint
	calibrated  bool
	members     int
}

// ResponseClusterer groups responses by status, content type and body
// similarity. It is safe for concurrent use.
type ResponseClusterer struct {
	mu       sync.Mutex
	opts     ClusterOptions
	nextID   int
	scopes   map[string][]*responseCluster
	clusters int
}

// NewResponseClusterer creates a clusterer. With no options it uses
// DefaultClusterOptions.
func NewResponseClusterer(opts ...ClusterOptions) *ResponseClusterer {
	o := DefaultClusterOptions().normalized()
	if len(opts) > 0 {
		o = opts[0].normalized()
	}
	return &ResponseClusterer{
		opts:   o,
		nextID: 1,
		scopes: make(map[string][]*responseCluster),
	}
}

// Threshold returns the effective similarity threshold.
func (c *ResponseClusterer) Threshold() float64 {
	if c == nil {
		return 0
	}
	return c.opts.Threshold
}

// ClusterCount returns the number of tracked clusters.
func (c *ResponseClusterer) ClusterCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.clusters
}

// Observe assigns resp to a cluster. The caller keeps the first response in an
// ordinary cluster and suppresses the remaining members, so the result can be
// rendered as one representative per distinct response shape.
func (c *ResponseClusterer) Observe(resp *Response) ClusterDecision {
	if c == nil || resp == nil {
		return ClusterDecision{Overflow: true}
	}
	fp := fingerprintResponse(resp, c.opts.MaxBodyBytes)
	scope := clusterScope(resp)

	c.mu.Lock()
	defer c.mu.Unlock()

	clusters := c.scopes[scope]
	best := -1
	bestScore := 0.0
	for i, cluster := range clusters {
		score := fingerprintSimilarity(fp, cluster.fingerprint)
		if score > bestScore {
			best = i
			bestScore = score
		}
	}
	if best >= 0 && bestScore >= c.opts.Threshold {
		cluster := clusters[best]
		cluster.members++
		return ClusterDecision{
			ClusterID:  cluster.id,
			Size:       cluster.members,
			Duplicate:  true,
			Calibrated: cluster.calibrated,
			dedupe:     c.opts.deduplicate,
		}
	}

	if len(clusters) >= c.opts.MaxClusters {
		return ClusterDecision{Overflow: true}
	}
	cluster := &responseCluster{
		id:          c.nextID,
		fingerprint: fp,
		members:     1,
	}
	c.nextID++
	c.scopes[scope] = append(clusters, cluster)
	c.clusters++
	return ClusterDecision{ClusterID: cluster.id, Size: 1}
}

// Calibrate installs autocalibration responses as known-noise clusters. Any
// later response similar to one of them is suppressed. Calibration intentionally
// does not consume the normal Observe path because probe responses must never be
// emitted or counted as scan results.
func (c *ResponseClusterer) Calibrate(responses []Response) {
	if c == nil || len(responses) == 0 {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for i := range responses {
		resp := &responses[i]
		fp := fingerprintResponse(resp, c.opts.MaxBodyBytes)
		scope := clusterScope(resp)
		clusters := c.scopes[scope]

		matched := false
		for _, cluster := range clusters {
			if fingerprintSimilarity(fp, cluster.fingerprint) >= c.opts.Threshold {
				cluster.calibrated = true
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		if len(clusters) >= c.opts.MaxClusters {
			continue
		}
		cluster := &responseCluster{
			id:          c.nextID,
			fingerprint: fp,
			calibrated:  true,
			members:     1,
		}
		c.nextID++
		c.scopes[scope] = append(clusters, cluster)
		c.clusters++
	}
}

// ResponseSimilarity estimates the similarity of two responses in [0, 1]. It is
// intended for tests and diagnostics; the hot path uses precomputed
// fingerprints.
func ResponseSimilarity(a, b *Response) float64 {
	if a == nil || b == nil {
		return 0
	}
	opts := DefaultClusterOptions()
	return fingerprintSimilarity(
		fingerprintResponse(a, opts.MaxBodyBytes),
		fingerprintResponse(b, opts.MaxBodyBytes),
	)
}

func fingerprintResponse(resp *Response, maxBody int) responseFingerprint {
	fp := responseFingerprint{
		status:      resp.StatusCode,
		contentType: normalizeContentType(resp.ContentType),
		empty:       len(resp.Data) == 0,
	}

	if fp.empty {
		location := ""
		if resp.Request != nil {
			location = resp.GetRedirectLocation(false)
			location = string(normalizeClusterText([]byte(location), resp.Request.Input))
		}
		fp.exact = hashBytes([]byte(fp.contentType + "\x00" + location))
		return fp
	}

	body := resp.Data
	if maxBody > 0 && maxBody < len(body) {
		body = body[:maxBody]
	}
	var inputs map[string][]byte
	if resp.Request != nil {
		inputs = resp.Request.Input
	}
	normalized := normalizeClusterText(body, inputs)
	fp.exact = hashBytes(normalized)
	if len(normalized) < clusterShingleSize {
		fp.short = true
		return fp
	}
	fp.simhash = simhash(normalized)
	return fp
}

func normalizeContentType(contentType string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}
	return contentType
}

// normalizeClusterText removes values supplied by the fuzzer and collapses
// whitespace/case. Soft-404 pages commonly echo the requested path; without
// masking it, every response looks different even though the page template is
// identical.
func normalizeClusterText(data []byte, inputs map[string][]byte) []byte {
	if len(inputs) > 0 {
		values := make([]string, 0, len(inputs)*3)
		seen := make(map[string]bool)
		for _, value := range inputs {
			if len(value) < clusterMinMaskValue {
				continue
			}
			for _, candidate := range []string{
				string(value),
				url.PathEscape(string(value)),
				url.QueryEscape(string(value)),
			} {
				if len(candidate) >= clusterMinMaskValue && !seen[candidate] {
					seen[candidate] = true
					values = append(values, candidate)
				}
			}
		}
		sort.Slice(values, func(i, j int) bool {
			if len(values[i]) != len(values[j]) {
				return len(values[i]) > len(values[j])
			}
			return values[i] < values[j]
		})
		for _, value := range values {
			data = bytes.ReplaceAll(data, []byte(value), []byte{0})
		}
	}

	out := make([]byte, 0, len(data))
	inSpace := false
	for _, b := range data {
		switch b {
		case ' ', '\t', '\r', '\n':
			if !inSpace {
				out = append(out, ' ')
				inSpace = true
			}
		default:
			inSpace = false
			if b >= 'A' && b <= 'Z' {
				b += 'a' - 'A'
			}
			out = append(out, b)
		}
	}
	return out
}

func fingerprintSimilarity(a, b responseFingerprint) float64 {
	if a.status != b.status || a.contentType != b.contentType {
		return 0
	}
	if a.empty || b.empty {
		if a.empty && b.empty && a.exact == b.exact {
			return 1
		}
		return 0
	}
	if a.exact == b.exact {
		return 1
	}
	if a.short || b.short {
		return 0
	}
	return 1 - float64(bits.OnesCount64(a.simhash^b.simhash))/64
}

func simhash(data []byte) uint64 {
	var weights [64]int
	add := func(h uint64) {
		for bit := 0; bit < 64; bit++ {
			if h&(uint64(1)<<uint(bit)) != 0 {
				weights[bit]++
			} else {
				weights[bit]--
			}
		}
	}

	shingles := len(data) - clusterShingleSize + 1
	stride := 1
	if shingles > clusterMaxShingles {
		stride = (shingles + clusterMaxShingles - 1) / clusterMaxShingles
	}
	for i := 0; i+clusterShingleSize <= len(data); i += stride {
		var h uint64
		for _, b := range data[i : i+clusterShingleSize] {
			h = h*clusterHashPrime + uint64(b)
		}
		add(mixHash(h))
	}
	var signature uint64
	for bit := 0; bit < 64; bit++ {
		if weights[bit] >= 0 {
			signature |= uint64(1) << uint(bit)
		}
	}
	return signature
}

func hashBytes(data []byte) uint64 {
	var h uint64 = 14695981039346656037
	for _, b := range data {
		h ^= uint64(b)
		h *= clusterHashPrime
	}
	return h
}

// mixHash is the SplitMix64 finalizer. The rolling polynomial alone maps the
// byte windows into a fairly structured range; mixing it makes the bottom-k
// MinHash sample uniform without adding another per-byte pass.
func mixHash(x uint64) uint64 {
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

func clusterScope(resp *Response) string {
	if resp == nil || resp.Request == nil {
		return ""
	}
	if parsed, err := url.Parse(resp.Request.Url); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host)
	}
	return strings.ToLower(resp.Request.Host)
}
