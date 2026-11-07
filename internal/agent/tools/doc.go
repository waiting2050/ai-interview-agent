// Package tools exposes the read-only conversation queries used by report
// generation: get_session_overview, search_turns, list_weak_turns and
// get_turn. The report's fixed collect plan calls the first three
// deterministically and hands the bundle to the model; get_turn is
// implemented but not used by that plan.
package tools
