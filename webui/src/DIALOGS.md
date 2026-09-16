# Application Dialogs

Both application shells mount one `DialogHost`. All page confirmations, prompts
and notices use `dialogs` from `dialogs.js`; browser `alert`, `confirm` and
`prompt` are forbidden by the source audit in `tests/dialogs.mjs`.

```js
if (!await dialogs.confirm(message)) return
const value = await dialogs.prompt(label, initialValue)
if (value === null) return
await dialogs.alert(message)
```

Always await the result. A prompt returns a string (including an empty string)
or `null` on cancellation. Confirm returns a boolean. Preserve every existing
step in destructive and paid-operation confirmation sequences.

The shared React component renders a styled HTML `dialog` in the page DOM.
It does not use browser chrome dialogs, replace globals, or interpret messages
as HTML. Escape cancels; focus returns to the opener. Navigation, authentication
expiry and shell unmount cancel pending dialogs. Concurrent opens are cancelled
instead of queuing duplicate destructive actions. Existing business APIs,
identity fencing, and permissions remain responsible for admission.
