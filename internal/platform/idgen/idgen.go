// Package idgen generates sortable identifiers. ULIDs keep creation order
// visible in logs and in primary-key indexes.
package idgen

import "github.com/oklog/ulid/v2"

func New() string {
	return ulid.Make().String()
}
