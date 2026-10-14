// Package emberplus implements an Ember+ consumer (client). It does not
// implement a provider.
//
// A Client connects to a provider, keeps a mirror of the provider tree and
// offers the operations of Ember+: browsing the tree, reading and setting
// parameter values, invoking functions, connecting matrix crosspoints and
// observing changes.
//
//	c, err := emberplus.Dial(ctx, "192.168.1.10:9000")
//	if err != nil {
//		return err
//	}
//	defer c.Close()
//
//	el, err := c.Get(ctx, "root/settings/info/version")
//
// # Paths
//
// Elements are addressed by path strings:
//
//   - identifier paths separated by "/": "root/settings/info"
//   - numeric paths separated by ".": "1.100.3", as printed by glow.Path
//   - mixed paths separated by "/": "root/100/info"
//
// In "/" separated paths, segments consisting of digits only are numbers,
// since identifiers must begin with a letter or "_". Identifiers may contain
// ".", as in "dsp/gain.left"; "." only separates purely numeric paths.
//
// The client resolves every path itself and loads missing parts of the tree
// with GetDirectory as needed; later accesses are served from the mirror.
// Numbers are only valid for one session, so applications should store
// identifier paths. IdentifierPath converts a path to its identifier form.
//
// # Observing changes
//
// A provider reports changes of all elements below a node once their
// directory has been loaded. Watch registers a callback for an element and
// loads what is needed for its changes to be reported.
//
// Stream parameters, typically audio level meters, are different: they only
// report values while subscribed, and usually do so every 50 to 80 ms.
// Subscribing is therefore never implicit. Use Subscribe to start a stream
// and Watch to receive its values; glow.Parameter.IsStream reports whether a
// parameter is a stream.
//
// # Concurrency
//
// All methods of Client are safe for concurrent use. The client reads from
// the connection in its own goroutine, answers keep-alive requests and
// delivers Watch callbacks from a separate goroutine, so a slow callback
// does not stall the connection.
//
// Ember+ has no request identifiers except for function invocations.
// Responses are therefore matched by path: a GetDirectory is answered by the
// first message reporting children of the requested node, a SetValue by the
// next value of the parameter. Requests that a provider never answers end
// when their context ends, or after the timeout set with WithRequestTimeout.
//
// # Packages
//
// The client builds on three packages that can also be used on their own:
// s101 for message framing, ber for the encoding and glow for the data
// schema.
//
// References such as (spec: "Keep-Alive mechanism") name chapters of the
// Ember+ specification ("Ember+ Documentation.pdf"), published by Lawo GmbH at
// https://github.com/Lawo/ember-plus/tree/master/documentation.
package emberplus
