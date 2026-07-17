//go:build js

package browser

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall/js"
)

// The JS interop primitives every entry point in this package is built from.
//
// One rule governs all of them, and breaking it deadlocks the host rather
// than failing it: the Go wasm runtime dispatches a js.Func callback on the
// same goroutine that drives the JS event loop, so a callback that blocks
// waiting on JS (a Promise, an IndexedDB request) waits for an event loop it
// is itself preventing from running. Every exported entry point therefore
// returns a Promise immediately and does its blocking work on a fresh
// goroutine — which is also exactly what the IndexedDB store's callback
// discipline (internal/edge/store/idb.go) already assumes of its callers.

// promise runs fn on a new goroutine and returns a JS Promise settled with
// its result: resolved with the returned value, rejected with an Error
// carrying the returned error's message. fn must not be called on the event
// loop goroutine, which is the whole point of the goroutine here.
func promise(fn func() (any, error)) js.Value {
	executor := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			// A panic on this goroutine would otherwise take down the whole
			// wasm instance with no diagnostic reaching the page: the
			// renderer's call would hang on a Promise that never settles.
			defer func() {
				if r := recover(); r != nil {
					reject.Invoke(newJSError(fmt.Sprintf("edge/browser: panic: %v", r)))
				}
			}()
			v, err := fn()
			if err != nil {
				reject.Invoke(newJSError(err.Error()))
				return
			}
			resolve.Invoke(v)
		}()
		return nil
	})
	// The executor runs synchronously inside Promise's constructor, so it has
	// already been invoked by the time New returns and can be released here —
	// the goroutine it spawned holds resolve/reject, not the executor itself.
	defer executor.Release()
	return js.Global().Get("Promise").New(executor)
}

func newJSError(msg string) js.Value {
	return js.Global().Get("Error").New(msg)
}

// await blocks the calling goroutine until p settles, and is the mirror of
// promise: it must be called from a goroutine, never from a js.Func callback.
// A value that is not a thenable is returned as-is, so a shell may implement
// a method synchronously without every caller here having to care.
func await(ctx context.Context, p js.Value) (js.Value, error) {
	if p.Type() != js.TypeObject || p.Get("then").Type() != js.TypeFunction {
		return p, nil
	}
	resCh := make(chan js.Value, 1)
	errCh := make(chan error, 1)
	settled := make(chan struct{})
	var once sync.Once
	done := func() { once.Do(func() { close(settled) }) }

	onOK := js.FuncOf(func(_ js.Value, a []js.Value) any {
		resCh <- firstArg(a)
		done()
		return nil
	})
	onErr := js.FuncOf(func(_ js.Value, a []js.Value) any {
		errCh <- rejectionError(a)
		done()
		return nil
	})
	// Released once the promise settles, NOT when this function returns: on a
	// ctx cancellation the promise is still outstanding, and JS calling a
	// released Func panics. A promise that never settles leaks this goroutine
	// and the two Funcs — bounded, and strictly better than that panic.
	go func() {
		<-settled
		onOK.Release()
		onErr.Release()
	}()
	p.Call("then", onOK, onErr)

	select {
	case v := <-resCh:
		return v, nil
	case err := <-errCh:
		return js.Undefined(), err
	case <-ctx.Done():
		return js.Undefined(), ctx.Err()
	}
}

func firstArg(a []js.Value) js.Value {
	if len(a) == 0 {
		return js.Undefined()
	}
	return a[0]
}

// rejectionError renders a promise rejection as a Go error. A rejection can
// carry anything at all (JS permits `reject(undefined)`), so this never
// assumes an Error instance.
func rejectionError(a []js.Value) error {
	if len(a) == 0 {
		return errors.New("promise rejected with no reason")
	}
	v := a[0]
	if v.Type() == js.TypeObject && v.Get("message").Type() == js.TypeString {
		return errors.New(v.Get("message").String())
	}
	if v.Type() == js.TypeString {
		return errors.New(v.String())
	}
	return fmt.Errorf("promise rejected with %v", v)
}

// toUint8Array copies b into a fresh JS Uint8Array.
func toUint8Array(b []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	return arr
}

// toBytes copies a JS Uint8Array back into Go. js.CopyBytesToGo panics on
// anything else, so the instance check is the error path, not a formality:
// the value crosses a host boundary this package does not control.
func toBytes(v js.Value) ([]byte, error) {
	if !v.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, fmt.Errorf("expected a Uint8Array, got %s", v.Type())
	}
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b, nil
}

// optString reads an optional string property, returning "" when absent.
func optString(o js.Value, name string) string {
	v := o.Get(name)
	if v.Type() != js.TypeString {
		return ""
	}
	return v.String()
}
