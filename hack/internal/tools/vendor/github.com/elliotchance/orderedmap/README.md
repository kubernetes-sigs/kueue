# 🔃 github.com/elliotchance/orderedmap/v3 [![GoDoc](https://godoc.org/github.com/elliotchance/orderedmap/v3?status.svg)](https://godoc.org/github.com/elliotchance/orderedmap/v3)

## Basic Usage

An `*OrderedMap` is a high performance ordered map that maintains amortized O(1)
for `Set`, `Get`, `Delete` and `Len`:

```go
import "github.com/elliotchance/orderedmap/v3"

func main() {
	m := orderedmap.NewOrderedMap[string, any]()

	m.Set("foo", "bar")
	m.Set("qux", 1.23)
	m.Set("123", true)

	m.Delete("qux")
}
```

> [!NOTE]
>
> - _v3 requires Go v1.23_ - If you need to support Go 1.18-1.22, you can use v2.
> - _v2 requires Go v1.18 for generics_ - If you need to support Go 1.17 or below, you can use v1.

Internally an `*OrderedMap` uses the composite type
[map](https://go.dev/blog/maps) combined with a
trimmed down linked list to maintain the order.

## Iterating

The following methods all return
[iterators](https://go.dev/doc/go1.23#iterators) that can be used to loop over
elements in an ordered map:

- `AllFromFront()`
- `AllFromBack()`
- `Keys()`
- `Values()`

```go
// Iterate through all elements from oldest to newest:
for key, value := range m.AllFromFront() {
	fmt.Println(key, value)
}
```

Iterators are safe to use bidirectionally, and will return `nil` once it goes
beyond the first or last item. If the map is changing while the iteration is
in-flight it may produce unexpected behavior.

If you want to get a slice of the map keys or values, you can use the standard
`slices.Collect` method with the iterator returned from `Keys()` or `Values()`:

```go
fmt.Println(slices.Collect(m.Keys())
// [A B C]
```

Likewise, calling `maps.Collect` on the iterator returned from `AllFromFront()`
will create a regular unordered map from the ordered one:

```go
fmt.Println(maps.Collect(m.AllFromFront())
// [A:1 B:2 C:3]
```

If you don't want to use iterators, you can also manually loop over the elements
using `Front()` or `Back()` with `Next()`:

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
