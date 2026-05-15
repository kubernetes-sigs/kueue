---
name: extract-helpers
description: Review that local variables with unnecessarily wide scope are extracted into helper functions.
license: Apache-2.0
metadata:
  copyright: The Kubernetes Authors
---

# Skill: Extract Helpers to Limit Variable Scope

**Flag:** A local variable whose scope spans more of a function than it needs to — it's only used inside a self-contained block of logic.

**Ask:** Extract that block into a helper function. This limits the variable's lifetime to the helper, shortens the enclosing function, and makes the intent clearer.

## Example

```go
func Greeting(user User) string {
    name := strings.TrimSpace(user.FirstName)
    if name == "" {
        name = "friend"
    }
    name = strings.Title(name)

    return fmt.Sprintf("Hello, %s! You have %d new messages.", name, user.Unread)
}
```

`name` is built up over three lines, then used once. Its scope spans the whole function for no reason. Real code tends to have more extreme versions of this.

vs.

```go
func displayName(user User) string {
    name := strings.TrimSpace(user.FirstName)
    if name == "" {
        return "friend"
    }
    return strings.Title(name)
}

func Greeting(user User) string {
    return fmt.Sprintf("Hello, %s! You have %d new messages.", displayName(user), user.Unread)
}
```

`name` now lives only inside `displayName`, and `Greeting` is a single expressive line.
