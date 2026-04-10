# Critical Rules Library

Standing rules derived from observed failure patterns in local model output.
These apply to ALL agent types and are injected into every prompt automatically.
Update this file as new failure patterns are discovered.

## Structural Integrity

1. **Never rewrite existing functions.** Hook into existing code by adding calls
   at the correct insertion point. If a function is 400+ lines, DO NOT delete and
   recreate it — add your logic to it.

2. **Every field you reference must exist.** Before using `o.someField` or
   `s.someField`, verify the struct definition contains that field. If it doesn't,
   add it to the struct AND wire it in the constructor.

3. **Every method you call must exist.** Do not call functions or methods that are
   not defined in the codebase. Check the Repository Map or grep for the function
   signature before calling it.

4. **Maintain brace balance.** Every `{` must have a matching `}`. Before finishing,
   verify all functions and blocks are properly closed.

## Go-Specific Rules

5. **Pointer vs value types.** `*uuid.UUID` and `uuid.UUID` are different types.
   When dereferencing a `*uuid.UUID` field, use `*field` to get the `uuid.UUID`
   value. Always nil-check pointer fields before dereferencing.

6. **Nil-guard optional dependencies.** When using an optional struct field
   (metrics store, supervisor, bus, etc.), ALWAYS check `if field != nil` before
   calling methods on it. The field may be nil in tests or minimal configurations.

7. **Pointer receivers for mutation.** If a method modifies struct fields (like
   `SetSize`), use a pointer receiver `func (v *Type)`, not a value receiver
   `func (v Type)`. Value receivers silently discard mutations.

8. **No variable shadowing of package names.** Do not name a variable the same as
   an imported package (e.g., `experiment := ...` when `experiment` is an imported
   package). Use a distinct name like `exp`.

## Bubble Tea / TUI Rules

9. **Never call the database in View().** Bubble Tea's `View()` is called on every
   render frame. Database queries go in `Init()` or `Update()` via `tea.Cmd`
   functions, with results cached in the model struct.

10. **Wire new views completely.** When adding a new view/screen:
    - Add the `Focus` constant to the enum
    - Add a `case` in the `handleKey` switch to delegate to the new handler
    - Add the handler function (with at minimum Escape returning to board)
    - Add a render call in the `View()` method's focus switch
    - Initialize the view in the `NewModel` constructor

## Code Quality

11. **Semantic correctness over syntactic correctness.** Put code in the right
    place. A "plan_rejected" metric belongs in the rejection handler, not the
    approval handler. Read the function name and surrounding comments before
    inserting code.

12. **Tests must have at least one assertion.** Do not write placeholder tests
    with empty bodies or no `assert`/`require` calls. Every test function must
    verify at least one observable behavior.

13. **Do not create files not listed in the task.** No `IMPLEMENTATION_SUMMARY.md`,
    no `NOTES.md`, no files beyond what the task brief specifies. Keep the working
    tree clean.

14. **GORM struct tags must be well-formed.** Ensure backtick-delimited struct
    tags have matched quotes and backticks. A malformed tag like
    `` `gorm:"column:foo;default:0"` `` with a missing closing backtick will
    cause silent schema errors.
