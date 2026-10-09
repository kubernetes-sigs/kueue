---
name: structured-errors
description: Flag error checking done via string comparison on err.Error() instead of structured/typed errors checked with errors.Is/errors.As.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Structured Errors

**Structured errors over string comparison** — error checking (especially in tests)
done via substring/equality checks on `err.Error()` instead of a sentinel or typed
error checked with `errors.Is` / `errors.As`. String-matching turns tests into
"change detector" tests, where a purely cosmetic wording tweak forces updates across
every test that asserted on it.

- A test asserting `err.Error() == "..."` or `strings.Contains(err.Error(), "...")`
  against a bare `errors.New(...)` / `fmt.Errorf(...)`. Prefer a sentinel
  (`var ErrFoo = errors.New(...)`) or a typed error with an `Is` method, checked with
  `errors.Is`.
- An error whose data (offending names, counts, a resource reference) only exists in
  the formatted message (`fmt.Errorf("...: %s", x)`) when a caller already needs that
  data. Prefer a typed error struct with the data as a field.
- A `cmp.Comparer` or similar helper that falls back to `strings.HasPrefix` to work
  around errors not being directly comparable — make the errors comparable instead
  (sentinel values or an `Is` method), and prefer `cmpopts.EquateErrors()` in tests.

Don't require a sentinel for a one-off error that's never compared (e.g. wrapped with
`%w` and only logged).
