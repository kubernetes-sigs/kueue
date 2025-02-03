# 🔃 github.com/elliotchance/orderedmap/v2 [![GoDoc](https://godoc.org/github.com/elliotchance/orderedmap/v2?status.svg)](https://godoc.org/github.com/elliotchance/orderedmap/v2)

## Basic Usage

An `*OrderedMap` is a high performance ordered map that maintains amortized O(1)
for `Set`, `Get`, `Delete` and `Len`:

```go
import "github.com/elliotchance/orderedmap/v2"

func main() {
	m := orderedmap.NewOrderedMap[string, any]()

	m.Set("foo", "bar")
	m.Set("qux", 1.23)
	m.Set("123", true)

	m.Delete("qux")
}
```

*Note: v2 requires Go v1.18 for generics.* If you need to support Go 1.17 or
below, you can use v1.

Internally an `*OrderedMap` uses the composite type
[map](https://go.dev/blog/maps) combined with a
trimmed down linked list to maintain the order.

## Iterating

Be careful using `Keys()` as it will create a copy of all of the keys so it's
only suitable for a small number of items:

```go
for _, key := range m.Keys() {
	value, _:= m.Get(key)
	fmt.Println(key, value)
}
```

For larger maps you should use `Front()` or `Back()` to iterate per element:

```go
// Iterate through all elements from oldest to newest:
for el := m.Front(); el != nil; el = el.Next() {
    fmt.Println(el.Key, el.Value)
}

// You can also use Back and Prev to iterate in reverse:
for el := m.Back(); el != nil; el = el.Prev() {
    fmt.Println(el.Key, el.Value)
}
```

In case you're using Go 1.23, you can also [iterate with
`range`](https://go.dev/doc/go1.23#iterators) by using `Iterator()` or
`ReverseIterator()` methods:

```go
for key, value := range m.Iterator() {
    fmt.Println(key, value)
}

for key, value := range m.ReverseIterator() {
    fmt.Println(key, value)
}
```

The iterator is safe to use bidirectionally, and will return `nil` once it goes
beyond the first or last item.

If the map is changing while the iteration is in-flight it may produce
unexpected behavior.
