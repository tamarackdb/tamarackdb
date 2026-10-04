// Package projection defines the shape of a TamarackDB projection: the
// current state an application computes from events, named by type and
// id, with an opaque payload and a version. It doesn't reuse package
// dcb's types: a projection has no query and no condition, only a key, a
// version, and a payload.
package projection
