// Package memory is the local/test implementation of ports (M1). It is
// single-process and non-durable; it exists so the app layer can run without
// PostgreSQL. This is the explicit replacement point for STORAGE=memory.
package memory
