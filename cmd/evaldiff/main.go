// Command evaldiff compares two existing eval result files without calling any
// model. It answers "did the candidate change metrics and which cases flipped
// band?" for A/B checks such as heuristic vs a real model.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
)

func main() {
	basePath := flag.String("base", "", "baseline result file (JSON)")
	candidatePath := flag.String("candidate", "", "candidate result file (JSON)")
	asJSON := flag.Bool("json", false, "print the comparison as JSON")
	flag.Parse()

	if *basePath == "" || *candidatePath == "" {
		fmt.Fprintln(os.Stderr, "evaldiff: -base and -candidate are required")
		os.Exit(1)
	}
	base, err := eval.ReadReport(*basePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaldiff: %v\n", err)
		os.Exit(1)
	}
	candidate, err := eval.ReadReport(*candidatePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "evaldiff: %v\n", err)
		os.Exit(1)
	}

	cmp := eval.Compare(base, candidate, *basePath, *candidatePath)
	if *asJSON {
		raw, err := json.MarshalIndent(cmp, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "evaldiff: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(raw))
		return
	}

	fmt.Printf("base:      %s\n", cmp.Base.Path)
	fmt.Printf("           provider=%s model=%s cases=%d gate=%v\n", cmp.Base.Provider, cmp.Base.Model, cmp.Base.Total, cmp.Base.GatePassed)
	fmt.Printf("candidate: %s\n", cmp.Candidate.Path)
	fmt.Printf("           provider=%s model=%s cases=%d gate=%v\n", cmp.Candidate.Provider, cmp.Candidate.Model, cmp.Candidate.Total, cmp.Candidate.GatePassed)
	fmt.Printf("gate_status_change: %s\n\n", cmp.GateStatusChange)

	fmt.Println("metrics:")
	for _, m := range cmp.Metrics {
		fmt.Printf("  %s\n", m.String())
	}

	fmt.Printf("\nflips: total=%d improved=%d regressed=%d changed=%d\n",
		len(cmp.Flips), cmp.ImprovedFlips, cmp.RegressedFlips, cmp.ChangedFlips)
	fmt.Printf("cases_only_in_base=%d cases_only_in_candidate=%d\n", cmp.BaseOnlyCases, cmp.CandidateOnly)
	for _, f := range cmp.Flips {
		fmt.Printf("  %-9s %-16s domain=%-10s band %d->%d (want %d) score %.1f->%.1f %s\n",
			f.Direction, f.CaseID, f.Domain, f.BaseBand, f.CandBand, f.ExpectedBand, f.BaseScore, f.CandScore, f.Topic)
	}
}
