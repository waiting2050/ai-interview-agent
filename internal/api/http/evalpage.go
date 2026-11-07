package httpapi

import (
	"embed"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/waiting2050/ai-interview-agent/internal/agent/eval"
)

//go:embed evalpanel/index.html
var evalPanelAssets embed.FS

var evalPanelTmpl = template.Must(template.New("eval").Funcs(template.FuncMap{
	"pct":  func(v float64) float64 { return v * 100 },
	"join": func(v []string) string { return strings.Join(v, "、") },
}).ParseFS(evalPanelAssets, "evalpanel/index.html"))

const defaultEvalResultsDir = "docs/eval-results"

// evalFileView is one result file listed in the panel.
type evalFileView struct {
	Name        string
	Provider    string
	Model       string
	GeneratedAt string
	Cases       int
	GatePassed  bool
	Selected    bool
	CompareBase bool
	CompareCand bool
}

// evalDomainSection groups misses by domain pack so the panel mirrors how the
// regression set is labelled.
type evalDomainSection struct {
	Domain string
	Cases  []eval.CaseResult
}

// evalPanelData is everything the server-rendered page needs. No JavaScript
// fetches data: the page is complete when the response leaves the server.
type evalPanelData struct {
	Files        []evalFileView
	Selected     *eval.Report
	SelectedName string
	Misses       []eval.CaseResult
	Sections     []evalDomainSection
	Compare      *eval.Comparison
	BaseName     string
	Candidate    string
	Dir          string
	LoadError    string
}

// evalPanel renders the read-only evaluation dashboard. It reads JSON result
// files from docs/eval-results (never the database) and performs no scoring.
func (h *handlers) evalPanel(w http.ResponseWriter, r *http.Request) {
	deps := h.deps
	dir := deps.EvalResultsDir
	if dir == "" {
		dir = defaultEvalResultsDir
	}
	data := evalPanelData{Dir: dir}

	files, err := listEvalReports(dir)
	if err != nil {
		data.LoadError = err.Error()
	} else {
		data.Files = files
	}

	selected := filepath.Base(strings.TrimSpace(r.URL.Query().Get("file")))
	if selected == "" || selected == "." {
		if len(files) > 0 {
			selected = files[0].Name
		}
	}
	for _, f := range files {
		if f.Name == selected {
			name := f.Name
			report, err := eval.ReadReport(filepath.Join(dir, name))
			if err != nil {
				data.LoadError = err.Error()
				break
			}
			data.Selected = &report
			data.SelectedName = name
			data.Misses = missedCases(report.Cases)
			data.Sections = groupMissesByDomain(data.Misses)
			break
		}
	}
	// Mark the selected file for the template.
	for i := range data.Files {
		data.Files[i].Selected = data.Files[i].Name == data.SelectedName
	}

	base := filepath.Base(strings.TrimSpace(r.URL.Query().Get("base")))
	cand := filepath.Base(strings.TrimSpace(r.URL.Query().Get("candidate")))
	if base != "" && cand != "" {
		baseReport, errBase := eval.ReadReport(filepath.Join(dir, base))
		candReport, errCand := eval.ReadReport(filepath.Join(dir, cand))
		if errBase != nil || errCand != nil {
			data.LoadError = "compare: " + joinErrors(errBase, errCand)
		} else {
			cmp := eval.Compare(baseReport, candReport, base, cand)
			data.Compare = &cmp
			data.BaseName = base
			data.Candidate = cand
		}
	}
	for i := range data.Files {
		data.Files[i].CompareBase = data.Files[i].Name == data.BaseName
		data.Files[i].CompareCand = data.Files[i].Name == data.Candidate
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := evalPanelTmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		deps.Logger.Error("render eval panel", "error", err)
	}
}

// listEvalReports returns *.json files newest first. Metadata is decoded so
// the list can show provider and gate status without opening each file later.
func listEvalReports(dir string) ([]evalFileView, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		view evalFileView
		mod  time.Time
	}
	var items []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		report, err := eval.ReadReport(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		mod := time.Time{}
		if info, err := e.Info(); err == nil {
			mod = info.ModTime()
		}
		items = append(items, candidate{
			view: evalFileView{
				Name:        e.Name(),
				Provider:    report.Provider,
				Model:       report.Model,
				GeneratedAt: report.GeneratedAt.Format("2006-01-02 15:04"),
				Cases:       report.Metrics.Total,
				GatePassed:  report.Gate.Passed,
			},
			mod: mod,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].view.Name != items[j].view.Name {
			return items[i].view.Name > items[j].view.Name
		}
		return items[i].mod.After(items[j].mod)
	})
	views := make([]evalFileView, 0, len(items))
	for _, it := range items {
		views = append(views, it.view)
	}
	return views, nil
}

func missedCases(cases []eval.CaseResult) []eval.CaseResult {
	var misses []eval.CaseResult
	for _, c := range cases {
		if c.Error != "" || c.Degraded || c.ActualBand != c.ExpectedBand {
			misses = append(misses, c)
		}
	}
	return misses
}

func groupMissesByDomain(cases []eval.CaseResult) []evalDomainSection {
	order := []string{eval.DomainGoBackend, eval.DomainAIAgent}
	group := map[string][]eval.CaseResult{}
	for _, c := range cases {
		group[c.Domain] = append(group[c.Domain], c)
	}
	var sections []evalDomainSection
	for _, d := range order {
		if len(group[d]) > 0 {
			sections = append(sections, evalDomainSection{Domain: d, Cases: group[d]})
			delete(group, d)
		}
	}
	// Unknown domains (for example result files written before domain was
	// added to the schema) are still shown rather than silently dropped.
	var rest []string
	for d := range group {
		rest = append(rest, d)
	}
	sort.Strings(rest)
	for _, d := range rest {
		sections = append(sections, evalDomainSection{Domain: d, Cases: group[d]})
	}
	return sections
}

func joinErrors(errs ...error) string {
	var parts []string
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return strings.Join(parts, "; ")
}
