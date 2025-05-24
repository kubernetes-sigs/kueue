# Copilot Guidelines -- General

1. Always prioritize clean, readable code over clever solutions.
2. Write self-documenting code with clear variable/function names instead of comments; use comments only in rare cases where logic cannot be made self-explanatory.
3. Before suggesting any code change, briefly explain your reasoning and the expected outcome.
4. Provide an estimate of the change size (small/medium/large); for medium/large or complex changes, first outline the approach, break it into smaller, manageable, sequential steps, and recommend which step to tackle first.
5. Focus on one specific task at a time - don't try to solve multiple problems simultaneously.
6. Prefer using modern language features but explain when and why they're better.
7. Assume I want to understand the logic behind your suggestions, not just the code.
8. If unsure about the usage of any external resource, library, or API, ask for official documentation before implementing.
9. When uncertain about a resolution, suggest specific debugging steps using a debugger, including what variables to watch and conditions to check.

# Copilot Guidelines -- GO
1. This is my very first experience with GO programming - keep modifications very short for each agent coding iteration.
2. I am familiar with Python, and a little with C, so you reference examples when appropriate.
3. This code is for an open-source project under the official k8s project. We should write product-quality code.

# Copilot Guidelines -- General
1. never share `cat` commands to insert content to files. Instead, suggest `nano path/to/file` and then tell the content to insert.
2. Note that I am using `fish` shell, not `bash`. On special cases, I can change to `bash` if you suggest it.
