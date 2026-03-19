# Shared demo actions — sourced by theme tapes.
# Do not run directly.

# Launch tori.
Type "tori"
Enter
Sleep 3s
Type "S"
Sleep 3s
Enter

# Dashboard view — browse containers.
Sleep 1s
Type "G"
Sleep 500ms
Type "{"
Sleep 500ms
Down
Sleep 500ms

# Expand api container.
Enter
Sleep 4s

# Go to alerts into container
Type "2"
Sleep 2s
Down
Sleep 500ms
Enter
Sleep 2s
Type "gd"
Sleep 3s

# Switch to other server
Type "S"
Sleep 2s
Down
Sleep 1s
Enter
Sleep 3s

# Enter group
Type "}"
Sleep 600ms
Enter
Sleep 3s
Type "s"
Sleep 2500ms
Escape
Sleep 1500ms

# Filter log stream
Type "/"
Sleep 1500ms
Type "failed"
Sleep 800ms
Enter
Sleep 1200ms
Enter
Sleep 2s
Up
Sleep 300ms
Up
Sleep 300ms
Up
Sleep 600ms
Escape
Sleep 500ms
Escape

# Display info
Sleep 800ms
Type "i"
Sleep 2s
Escape
Sleep 500ms
Escape

# Fold
Sleep 1s
Type "}"
Sleep 500ms
Space
Sleep 400ms
Type "}"
Sleep 300ms
Space
Sleep 800ms
Type "?"
Sleep 2500ms
Escape
Sleep 800ms

# exit
Type "q"
Sleep 1s

