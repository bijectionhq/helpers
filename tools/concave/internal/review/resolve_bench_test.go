package review_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"concave/internal/analysis"
	"concave/internal/review"
	"concave/internal/testrepo"
)

// Small histories isolate lookup cost. Setup and writes are outside measurement;
// timings are observations, never artifact admission requirements.
func BenchmarkContributionLookup(b *testing.B) {
	for _, count := range []int{1, 3, 12} {
		b.Run(fmt.Sprintf("members=%d", count), func(b *testing.B) {
			f := testrepo.New(b)
			repo, _ := f.Open()
			r := &review.Contribution{Format: review.ContributionFormat, Context: &analysis.ContractReview{Blobs: map[string]string{"source": strings.Repeat("x", 8192)}}}
			var origin, last string
			var size int64
			for i := 0; i < count; i++ {
				r.Parent, r.Origin = last, origin
				path, err := review.Store(repo, r)
				if err != nil {
					b.Fatal(err)
				}
				info, err := os.Stat(path)
				if err != nil {
					b.Fatal(err)
				}
				size += info.Size()
				if origin == "" {
					origin = r.ID
				}
				last = r.ID
			}
			for _, mode := range []string{"exact", "logical"} {
				b.Run(mode, func(b *testing.B) {
					ref := last
					if mode == "logical" {
						ref = "@" + origin
					}
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						got, err := review.Read(repo, ref)
						if err != nil || got.ID != last {
							b.Fatal(got, err)
						}
					}
					b.StopTimer()
					b.ReportMetric(float64(size), "history-bytes")
				})
			}
		})
	}
}
