// Util functions for type conversion between byte array and string without copy.
//
// Example usage 1 (convert string to byte slice):
// func TakeByteSlice(bs []byte) {...}
//
// func f() {
//   s := "helloworld"
//   TakeByteSlice(ConvertStringToByteSlice(s))  // convert string to byte slice with zero-copy
// }
//
// Example usage 2 (convert byte slice to string):
// func TakeString(s string) {...}
//
// func f() {
//   bytes := []byte("helloworld")
//   TakeByteSlice(ConvertByteSliceToString(bytes))  // convert byte slice to string with zero-copy
// }

package utils

import (
	"unsafe"
)

// Convert a byte array to string w/o copy.
//
// WARNING: The input byte slice is not expected to change.
func ConvertByteSliceToString(arr []byte) string {
	return unsafe.String(&arr[0], len(arr))
}

// Convert a string to byte array w/o copy.
//
// WARNING: The returned byte slice is not expected to change.
func ConvertStringToByteSlice(s string) (arr []byte) {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}
