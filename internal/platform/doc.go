// Package platform gathers what supports the application without belonging to
// the domain: configuration, structured logging, metrics and the Fx modules that
// wire the graph.
//
// It is the only place that knows about Fx. The domain compiles and is tested
// without a dependency injection container existing at all.
package platform
