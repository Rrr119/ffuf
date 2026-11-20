package ffuf

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func clusterTestResponse(value, body string) Response {
	return Response{
		StatusCode:  200,
		ContentType: "text/html; charset=utf-8",
		Data:        []byte(body),
		Request: &Request{
			Url:   "https://example.test/" + value,
			Host:  "example.test",
			Input: map[string][]byte{"FUZZ": []byte(value)},
		},
	}
}

func TestConfigFromOptions_ResponseCluster(t *testing.T) {
	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.test/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/wordlist"}
	opts.General.ResponseCluster = true
	if err := opts.General.ResponseClusterThreshold.Set("0.92"); err != nil {
		t.Fatalf("threshold Set: %v", err)
	}

	conf, err := ConfigFromOptions(opts, nil, nil)
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.ResponseCluster || conf.ResponseClusterThreshold != 0.92 {
		t.Fatalf("cluster config = (%t, %v), want (true, 0.92)", conf.ResponseCluster, conf.ResponseClusterThreshold)
	}
}

func TestConfigFromOptions_ResponseClusterThresholdImpliesCluster(t *testing.T) {
	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.test/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/wordlist"}
	if err := opts.General.ResponseClusterThreshold.Set("0.75"); err != nil {
		t.Fatalf("threshold Set: %v", err)
	}

	conf, err := ConfigFromOptions(opts, nil, nil)
	if err != nil {
		t.Fatalf("ConfigFromOptions: %v", err)
	}
	if !conf.ResponseCluster {
		t.Fatal("-rct should imply response clustering")
	}
	if conf.ResponseClusterThreshold != 0.75 {
		t.Fatalf("threshold = %v, want 0.75", conf.ResponseClusterThreshold)
	}
}

func TestConfigFromOptions_ResponseClusterThresholdValidation(t *testing.T) {
	for _, value := range []string{"0", "-0.1", "1.1", "not-a-number"} {
		t.Run(value, func(t *testing.T) {
			var threshold SimilarityThreshold
			if err := threshold.Set(value); err == nil {
				t.Fatalf("threshold %q should be rejected", value)
			}
		})
	}

	opts := NewConfigOptions()
	opts.HTTP.URL = "https://example.test/FUZZ"
	opts.Input.Wordlists = []string{"/tmp/wordlist"}
	opts.General.ResponseClusterThreshold = SimilarityThreshold{Value: 1.1}
	if _, err := ConfigFromOptions(opts, nil, nil); err == nil {
		t.Fatal("out-of-range threshold should be rejected by ConfigFromOptions")
	}
}

func TestReadConfig_ResponseClusterThresholdNumeric(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ffufrc")
	if err := os.WriteFile(path, []byte("[general]\nresponse_cluster = true\nresponse_cluster_threshold = 0.9\n"), 0644); err != nil {
		t.Fatal(err)
	}
	opts, err := ReadConfig(path)
	if err != nil {
		t.Fatalf("ReadConfig: %v", err)
	}
	if !opts.General.ResponseClusterThreshold.Explicit || opts.General.ResponseClusterThreshold.Value != 0.9 {
		t.Fatalf("threshold = %+v, want explicit 0.9", opts.General.ResponseClusterThreshold)
	}
}

func TestSimilarityThresholdJSONRoundTrip(t *testing.T) {
	var threshold SimilarityThreshold
	if err := threshold.Set("0.93"); err != nil {
		t.Fatal(err)
	}
	blob, err := json.Marshal(threshold)
	if err != nil {
		t.Fatal(err)
	}
	var restored SimilarityThreshold
	if err := json.Unmarshal(blob, &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Explicit || restored.Value != 0.93 {
		t.Fatalf("restored threshold = %+v, want explicit 0.93", restored)
	}
}

func TestResponseClusterer_SimilarReflectedResponses(t *testing.T) {
	clusterer := NewResponseClusterer()
	first := clusterTestResponse("alpha",
		"<html><title>Not found</title><body>No page named alpha exists. Return to the home page.</body></html>")
	second := clusterTestResponse("a-much-longer-payload",
		"<html><title>Not found</title><body>No page named a-much-longer-payload exists. Return to the home page.</body></html>")

	a := clusterer.Observe(&first)
	b := clusterer.Observe(&second)

	if !a.Keep() {
		t.Fatalf("first cluster representative should be kept: %+v", a)
	}
	if b.Keep() {
		t.Fatalf("similar reflected response should be suppressed: %+v", b)
	}
	if a.ClusterID == 0 || a.ClusterID != b.ClusterID {
		t.Fatalf("cluster ids = %d and %d, want the same non-zero id", a.ClusterID, b.ClusterID)
	}
	if b.Size != 2 {
		t.Fatalf("second response cluster size = %d, want 2", b.Size)
	}
}

func TestResponseClusterer_DistinctResponses(t *testing.T) {
	clusterer := NewResponseClusterer()
	first := clusterTestResponse("alpha", "alpha response with a completely separate body")
	second := clusterTestResponse("beta", "beta response: welcome to the administration console")

	a := clusterer.Observe(&first)
	b := clusterer.Observe(&second)
	if !a.Keep() || !b.Keep() {
		t.Fatalf("distinct responses should both be kept: %+v %+v", a, b)
	}
	if a.ClusterID == b.ClusterID {
		t.Fatalf("distinct responses shared cluster id %d", a.ClusterID)
	}
}

func TestResponseClusterer_AutocalibrationOnlyDoesNotDeduplicate(t *testing.T) {
	clusterer := NewResponseClusterer(ClusterOptions{
		Threshold:           DefaultResponseClusterThreshold,
		AutoCalibrationOnly: true,
	})
	first := clusterTestResponse("one", "identical ordinary response")
	second := clusterTestResponse("two", "identical ordinary response")
	if d := clusterer.Observe(&first); !d.Keep() {
		t.Fatalf("first ordinary response should be kept: %+v", d)
	}
	if d := clusterer.Observe(&second); !d.Keep() {
		t.Fatalf("autocalibration-only mode should not suppress ordinary duplicates: %+v", d)
	}

	probe := clusterTestResponse("probe", "calibrated baseline response")
	clusterer.Calibrate([]Response{probe})
	baseline := clusterTestResponse("another-probe", "calibrated baseline response")
	if d := clusterer.Observe(&baseline); d.Keep() {
		t.Fatalf("calibrated baseline should still be suppressed: %+v", d)
	}
}

func TestResponseClusterer_PartialBodyVariance(t *testing.T) {
	clusterer := NewResponseClusterer()
	prefix := strings.Repeat("<li class=\"nav-item\">navigation</li>", 64)
	first := clusterTestResponse("one", prefix+"<p>request id: 1001</p>")
	second := clusterTestResponse("two", prefix+"<p>request id: 2002</p>")

	if d := clusterer.Observe(&first); !d.Keep() {
		t.Fatalf("first response should be kept: %+v", d)
	}
	if d := clusterer.Observe(&second); d.Keep() {
		t.Fatalf("responses with a small dynamic suffix should cluster together: %+v", d)
	}
}

func TestResponseClusterer_ThresholdIsApplied(t *testing.T) {
	clusterer := NewResponseClusterer(ClusterOptions{Threshold: 0.999})
	prefix := strings.Repeat("<li class=\"nav-item\">navigation</li>", 64)
	first := clusterTestResponse("one", prefix+"<p>request id: 1001</p>")
	second := clusterTestResponse("two", prefix+"<p>request id: 2002</p>")
	if d := clusterer.Observe(&first); !d.Keep() {
		t.Fatalf("first response should be kept: %+v", d)
	}
	if d := clusterer.Observe(&second); !d.Keep() {
		t.Fatalf("a near-exact threshold should keep a partially different response: %+v", d)
	}
}

func TestResponseClusterer_StatusAndTypeSeparateClusters(t *testing.T) {
	clusterer := NewResponseClusterer()
	first := clusterTestResponse("alpha", "identical body")
	second := clusterTestResponse("beta", "identical body")
	second.StatusCode = 404
	if d := clusterer.Observe(&first); !d.Keep() {
		t.Fatalf("first response should be kept: %+v", d)
	}
	if d := clusterer.Observe(&second); !d.Keep() {
		t.Fatalf("different status must form a new cluster: %+v", d)
	}

	third := clusterTestResponse("gamma", "identical body")
	third.ContentType = "application/json"
	if d := clusterer.Observe(&third); !d.Keep() {
		t.Fatalf("different content type must form a new cluster: %+v", d)
	}
}

func TestResponseClusterer_CalibratedBaseline(t *testing.T) {
	clusterer := NewResponseClusterer()
	probeA := clusterTestResponse("calibration-alpha",
		"<html>No page named calibration-alpha was found.</html>")
	probeB := clusterTestResponse("calibration-beta-with-a-longer-name",
		"<html>No page named calibration-beta-with-a-longer-name was found.</html>")
	clusterer.Calibrate([]Response{probeA, probeB})

	junk := clusterTestResponse("actual-missing-page",
		"<html>No page named actual-missing-page was found.</html>")
	real := clusterTestResponse("admin",
		"<html><h1>Administration</h1><p>Dashboard and settings.</p></html>")

	if d := clusterer.Observe(&junk); d.Keep() {
		t.Fatalf("response similar to the calibrated baseline should be suppressed: %+v", d)
	} else if !d.Calibrated {
		t.Fatalf("baseline decision should be marked calibrated: %+v", d)
	}
	if d := clusterer.Observe(&real); !d.Keep() {
		t.Fatalf("distinct response should still be kept: %+v", d)
	}
}

func TestResponseClusterer_HostScoped(t *testing.T) {
	clusterer := NewResponseClusterer()
	first := clusterTestResponse("alpha", "same body")
	first.Request.Url = "https://one.example/alpha"
	second := clusterTestResponse("beta", "same body")
	second.Request.Url = "https://two.example/beta"

	a := clusterer.Observe(&first)
	b := clusterer.Observe(&second)
	if !a.Keep() || !b.Keep() {
		t.Fatalf("responses from different hosts must not share a cluster: %+v %+v", a, b)
	}
}

func TestResponseClusterer_EmptyBodiesUseRedirect(t *testing.T) {
	clusterer := NewResponseClusterer()
	first := Response{
		StatusCode:  301,
		ContentType: "text/html",
		Headers:     map[string][]string{"Location": {"/one"}},
		Request:     &Request{Url: "https://example.test/one"},
	}
	same := first
	same.Headers = map[string][]string{"Location": {"/one"}}
	same.Request = &Request{Url: "https://example.test/two"}
	different := first
	different.Headers = map[string][]string{"Location": {"/elsewhere"}}
	different.Request = &Request{Url: "https://example.test/three"}

	if d := clusterer.Observe(&first); !d.Keep() {
		t.Fatalf("first empty-body response should be kept: %+v", d)
	}
	if d := clusterer.Observe(&same); d.Keep() {
		t.Fatalf("same redirect should be a duplicate: %+v", d)
	}
	if d := clusterer.Observe(&different); !d.Keep() {
		t.Fatalf("different redirect should start a new cluster: %+v", d)
	}
}

func TestResponseClusterer_ConcurrentObserve(t *testing.T) {
	clusterer := NewResponseClusterer()
	const n = 64
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			value := fmt.Sprintf("payload-%d", i)
			resp := clusterTestResponse(value, "not found: "+value+" - try another path")
			clusterer.Observe(&resp)
		}(i)
	}
	wg.Wait()

	if got := clusterer.ClusterCount(); got != 1 {
		t.Fatalf("ClusterCount = %d, want 1", got)
	}
}

func TestResponseClusterer_MaxClustersKeepsOverflow(t *testing.T) {
	clusterer := NewResponseClusterer(ClusterOptions{
		Threshold:    0.99,
		MaxBodyBytes: 1024,
		MaxClusters:  1,
	})
	first := clusterTestResponse("one", "first unique response")
	second := clusterTestResponse("two", "completely different content")
	clusterer.Observe(&first)
	d := clusterer.Observe(&second)
	if !d.Keep() || !d.Overflow {
		t.Fatalf("overflow decision = %+v, want Keep and Overflow", d)
	}
}

func BenchmarkResponseSimilarity(b *testing.B) {
	left := clusterTestResponse("left", strings.Repeat("<div class=\"row\">some page content</div>", 4096))
	right := clusterTestResponse("right", strings.Repeat("<div class=\"row\">some page content</div>", 4096))
	b.ReportAllocs()
	b.SetBytes(int64(len(left.Data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := ResponseSimilarity(&left, &right); got != 1 {
			b.Fatalf("similarity = %v, want 1", got)
		}
	}
}

func BenchmarkResponseClustererObserve(b *testing.B) {
	clusterer := NewResponseClusterer()
	body := strings.Repeat("<div class=\"row\">some page content</div>", 4096)
	resp := clusterTestResponse("payload", body)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clusterer.Observe(&resp)
	}
}

func BenchmarkResponseClustererCalibrate(b *testing.B) {
	responses := make([]Response, 16)
	for i := range responses {
		value := fmt.Sprintf("calibration-%d", i)
		responses[i] = clusterTestResponse(value, "soft 404 for "+value+": return to the home page")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clusterer := NewResponseClusterer()
		clusterer.Calibrate(responses)
	}
}
