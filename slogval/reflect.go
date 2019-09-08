package slogval

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/fatih/camelcase"
	"github.com/golang/protobuf/proto"
	"golang.org/x/xerrors"

	"go.coder.com/slog"
)

func Reflect(fs []slog.Field) Map {
	var m Map
	for _, f := range fs {
		m = m.Append(f.LogKey(), reflectValue(reflect.ValueOf(f.LogValue())))
	}
	return m
}

// TODO
func ReflectMap() {
	panic("TODO")
}

// TODO remove later.
func ReflectUnsafe(v interface{}) Value {
	return reflectValue(reflect.ValueOf(v))
}

func reflectValue(rv reflect.Value) Value {
	if !rv.IsValid() {
		// reflect.ValueOf(nil).IsValid == false
		return nil
	}

	// We always want to look at the actual type in the interface.
	// Otherwise we cannot check if e.g. an error variable implements
	// the Value interface. If this statement was not here, we would see
	// the error variable always does not implement the Value interface
	// but does implement Error. With this, we check the concrete value instead.
	if rv.Kind() == reflect.Interface {
		return reflectValue(rv.Elem())
	}

	typ := rv.Type()
	switch {
	case implements(typ, (*Value)(nil)):
		v := rv.MethodByName("isSlogCoreValue").Call(nil)
		return v[0].Interface().(Value)
	case implements(typ, (*slog.Value)(nil)):
		m := rv.MethodByName("LogValue")
		lv := m.Call(nil)
		return reflectValue(lv[0])
	case implements(typ, (*xerrors.Formatter)(nil)):
		return extractErrorChain(rv)
	case implements(typ, (*error)(nil)):
		m := rv.MethodByName("Error")
		s := m.Call(nil)
		return String(s[0].String())
	case implements(typ, (*fmt.Stringer)(nil)):
		if implements(typ, (*proto.Message)(nil)) {
			// We do not want a flat string for protobufs.
			// The reflection based struct handler below will ensure
			// protobufs values have structure in logs.
			break
		}
		s := rv.MethodByName("String").Call(nil)
		return String(s[0].String())
	}

	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		if rv.IsNil() {
			return nil
		}
	}

	switch rv.Kind() {
	case reflect.String:
		return String(rv.String())
	case reflect.Bool:
		return Bool(rv.Bool())
	case reflect.Float32, reflect.Float64:
		return Float(rv.Float())
	case reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Int:
		return Int(rv.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return Uint(rv.Uint())
	case reflect.Ptr:
		return reflectValue(rv.Elem())
	case reflect.Slice, reflect.Array:
		// Ordered map.
		if typ == reflect.TypeOf([]slog.Field(nil)) {
			m := make(Map, 0, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				f := rv.Index(i)
				key := f.MethodByName("LogKey").Call(nil)[0].String()
				val := f.MethodByName("LogValue").Call(nil)[0]
				m = m.Append(key, reflectValue(val))
			}
			return m
		}
		list := make(List, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			list[i] = reflectValue(rv.Index(i))
		}
		return list
	case reflect.Map:
		m := make(Map, 0, rv.Len())
		for _, k := range rv.MapKeys() {
			mv := rv.MapIndex(k)
			m = m.Append(fmt.Sprintf("%v", k), reflectValue(mv))
		}
		m.Sort()
		return m
	case reflect.Struct:
		typ := rv.Type()

		f := make(Map, 0, typ.NumField())

		for i := 0; i < typ.NumField(); i++ {
			ft := typ.Field(i)
			fv := rv.Field(i)

			if ft.Tag.Get("log") == "-" {
				continue
			}
			if implements(typ, (*proto.Message)(nil)) && strings.HasPrefix(ft.Name, "XXX_") {
				// Have to ignore XXX_ fields for protobuf messages.
				continue
			}

			v := reflectValue(fv)
			name := ft.Tag.Get("log")
			if name == "" {
				name = snakecase(ft.Name)
			}
			f = f.Append(name, v)

		}

		return f
	default:
		return String(fmt.Sprintf("%v", rv))
	}
}

func snakecase(s string) string {
	splits := camelcase.Split(s)
	for i, s := range splits {
		splits[i] = strings.ToLower(s)
	}
	return strings.Join(splits, "_")
}

// This function requires that variable v be a pointer
// to the interface value that typ may implement.
// See https://blog.golang.org/laws-of-reflection
// Its the only way to grab the reflect type of an interface
// as using reflect.TypeOf on an interface grabs the
// type of its underlying value. So we pass a pointer
// to an interface and then use the Elem() method on the
// pointer reflect type to grab the type of the interface.
func implements(typ reflect.Type, v interface{}) bool {
	return typ.Implements(reflect.TypeOf(v).Elem())
}

type xerrorPrinter struct {
	msg      string
	function string
	// file:line
	loc string
}

func (p *xerrorPrinter) Print(v ...interface{}) {
	s := fmt.Sprint(v...)
	p.write(s)
}

func (p *xerrorPrinter) write(s string) {
	s = strings.TrimSpace(s)
	switch {
	case p.msg == "":
		p.msg = s
	case p.function == "":
		p.function = s
	case p.loc == "":
		p.loc = s
	default:
		panicf("unexpected String from xerrors.FormatError: %q", s)
	}
}

func (p *xerrorPrinter) Printf(f string, v ...interface{}) {
	s := fmt.Sprintf(f, v...)
	p.write(s)
}

func (p *xerrorPrinter) Detail() bool {
	return true
}

func (p *xerrorPrinter) fields() Value {
	s := p.msg
	if p.function != "" {
		s += "\n" + p.function
	}
	if p.loc != "" {
		s += "\n  " + p.loc
	}
	return String(s)
}

// The value passed in must implement xerrors.Formatter.
func extractErrorChain(rv reflect.Value) List {
	errs := List{}

	formatError := func(p xerrors.Printer) error {
		m := rv.MethodByName("FormatError")
		err := m.Call([]reflect.Value{reflect.ValueOf(p)})
		next, _ := err[0].Interface().(error)
		return next
	}
	for {
		p := &xerrorPrinter{}
		err := formatError(p)

		errs = append(errs, p.fields())

		if err != nil {
			var ok bool
			f, ok := err.(xerrors.Formatter)
			if ok {
				formatError = func(p xerrors.Printer) error {
					return f.FormatError(p)
				}
				continue
			}
			errs = append(errs, reflectValue(reflect.ValueOf(err)))
		}
		return errs
	}
}

func panicf(f string, v ...interface{}) {
	f = "slogval: " + f
	s := fmt.Sprintf(f, v...)
	panic(s)
}