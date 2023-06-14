# middlewarer
Generate a middleware framework for any Go interface

# Example

`example.go`:
```go
// go:generate middlewarer -type=Foo
type Foo interface {
    Bar(Baz) Quz
}
```

`example_middleware.go`
```go
// WrapFoo wraps the methods of the provided interface
// in the provided middleware
func WrapFoo(in Foo, m FooMiddleware) Foo {
    m.wrapped = in
    return &m
}

// FooMiddleware implements Foo
type FooMiddleware struct {
    wrapped Foo

    BarMiddleware func(BarHandler) BarHandler
}

type BarHandler func(Baz) Quz

func (m *FooMiddleware) Bar(a0 Baz) Quz {
    fun := m.wrapped.Bar
    if m.BarMiddleware != nil {
        fun = m.BarMiddleware(fun)
    }
    return fun(a0)
}
```

# Adding Middleware

`logger.go`
```go
func AddLoggerToFoo(f Foo) Foo {
    return WrapFoo(f, FooMiddleware{
        BarMiddleware: barLogger
    })
}

func barLogger(next BarHandler) BarHandler {
    return func(b Baz) Quz {
        log.Println("Calling Bar!")
        res := next()
        log.Println("Bar returned: ", res)
        return res
    }
}
```

# Optional Implementation

When wrapping an instance of an interface `<I>` by calling `Wrap<I>`, the provided struct `<I>Middleware` is allowed to have fields evaluating to `nil`.

This allows adding middleware to select methods.

Take for example 

```go
// go:generate middlewarer -type=Server
type Server interface {
    Init()
    Request() Response
}
```

If you now want to wrap the `Request` method with middleware, but not the `Init` method, you can simply do:

```go
s := getServer()

s = WrapServer(s, ServerMiddleware{
    // InitMiddleware: nil
    RequestMiddleware: someMiddlewareFunc
})
```

When calling `Init` on `s`, no middleware gets called.
However, when calling `Request`, the passed middleware function gets invoked.