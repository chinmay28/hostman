/**
 * Putting text on the clipboard.
 *
 * The contract is the return value: true only when the text is known to have
 * reached the clipboard, false when every way of getting it there was refused.
 * A caller that hears true may tell the user so. A caller that hears false has
 * to offer another way — a box of text the phone's own Copy can reach.
 *
 * "Known" is the point. The old copy command answers true whenever it was
 * allowed to run, whether or not it copied anything, and Safari is happy to
 * run it over an empty selection: that is how a terminal came to say "Copied
 * one line" to a phone with nothing on its clipboard. So the old way here is
 * never trusted on its say-so — it counts only when the browser came back
 * through the copy event and took the text from our hands.
 */

/** Call from inside the user's tap or click. Both ways need the gesture, and
 *  the second is gone the moment anything is awaited before it. */
export async function toClipboard(text: string): Promise<boolean> {
  // The modern way needs a page the browser trusts — https or localhost — and
  // HostMan is usually plain http on a LAN, where the object does not exist.
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Refused. The old way may still be allowed.
    }
  }
  return byCommand(text)
}

/**
 * The copy command, the way that still works on a plain http page.
 *
 * Two things have to be true for it to copy. There has to be a selection,
 * because Safari will not run the command over none — and it has to be a real
 * one over text in the page, since setting the selection of a text box that
 * is not focused no longer moves the document's selection at all. And what it
 * copies should not be the selection: a listener hands over the exact text on
 * the copy event, so whitespace survives and so the return value can mean
 * something — the listener running is the proof that the command ran.
 */
function byCommand(text: string): boolean {
  if (typeof document.execCommand !== 'function') return false

  const stage = document.createElement('span')
  stage.textContent = text
  // Out of sight but in the layout, and selectable whatever the page says:
  // a selection over a node the page marks unselectable collapses to nothing.
  stage.style.cssText =
    'position:fixed;top:0;left:0;opacity:0;pointer-events:none;white-space:pre;' +
    'user-select:text;-webkit-user-select:text'
  document.body.appendChild(stage)

  const selection = window.getSelection()
  // Whatever the page had selected goes back afterwards; this is a copy, not
  // a click somewhere else.
  const had = selection && selection.rangeCount > 0 ? selection.getRangeAt(0) : null
  const range = document.createRange()
  range.selectNodeContents(stage)
  selection?.removeAllRanges()
  selection?.addRange(range)

  let placed = false
  const onCopy = (e: ClipboardEvent) => {
    if (!e.clipboardData) return
    e.clipboardData.setData('text/plain', text)
    e.preventDefault()
    placed = true
  }
  // Capture, so it runs before anything on the page that answers copy events
  // itself — the terminal does — and cannot be stopped by it.
  document.addEventListener('copy', onCopy, true)
  try {
    document.execCommand('copy')
  } catch {
    // Not allowed here. placed stays false.
  } finally {
    document.removeEventListener('copy', onCopy, true)
    selection?.removeAllRanges()
    if (had) selection?.addRange(had)
    stage.remove()
  }
  return placed
}
