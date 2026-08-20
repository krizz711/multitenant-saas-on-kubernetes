// Package controlplane exists only to document and to guard the boundary its
// subpackages sit behind.
//
// Everything under controlplane/ is application-agnostic: it keys off an HTTP
// header, a path classification and a queue depth, and knows nothing about
// what the application it governs actually does. Everything under workload/ is
// the demonstration application, which is deliberately small, frozen once it
// works, and replaceable.
//
// The dependency runs one way only - workload imports controlplane, never the
// reverse - and TestControlPlaneDoesNotImportWorkload enforces that rather
// than trusting it. That test failing means the deliverable has grown a
// dependency on the thing it is supposed to be independent of.
package controlplane
