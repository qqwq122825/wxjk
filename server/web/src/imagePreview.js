// Keep the displayed frame until its replacement is fully downloaded and decoded.
// Only one live-frame request runs at a time; newer live frames replace the queue,
// not the in-flight download (otherwise a slow connection can starve forever).
export async function loadPreviewImage(url, signal) {
  const response = await fetch(url, { credentials: 'same-origin', cache: 'no-store', signal })
  if (!response.ok) throw new Error(`Image request failed (${response.status})`)
  const blob = await response.blob()
  if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
  const objectURL = URL.createObjectURL(blob)
  try {
    await new Promise((resolve, reject) => {
      const image = new globalThis.Image()
      let settled = false
      const finish = error => {
        if (settled) return
        settled = true
        signal.removeEventListener('abort', abort)
        image.onload = image.onerror = null
        error ? reject(error) : resolve()
      }
      const abort = () => finish(new DOMException('Aborted', 'AbortError'))
      signal.addEventListener('abort', abort, { once: true })
      image.onerror = () => finish(new Error('Image decode failed'))
      if (typeof image.decode !== 'function') image.onload = () => finish()
      image.src = objectURL
      if (signal.aborted) abort()
      else if (typeof image.decode === 'function') image.decode().then(() => finish(), finish)
    })
    return { url: objectURL, contentType: blob.type, dispose: () => URL.revokeObjectURL(objectURL) }
  } catch (error) {
    URL.revokeObjectURL(objectURL)
    throw error
  }
}

export function imageFormat(frame) {
  if (frame?.content_type === 'image/jpeg') return 'JPG'
  if (frame?.content_type === 'image/png') return 'PNG'
  return /^(jpe?g)$/i.test(frame?.format || '') ? 'JPG' : 'PNG'
}

export function createImagePreview({ load = loadPreviewImage, onChange = () => {}, onError = () => {}, timeoutMs = 10000 } = {}) {
  let epoch = 0, scope, running, queued, current, lastKey, stopped = false
  function invalidate(clear) {
    epoch++
    clearTimeout(running?.timer)
    running?.controller.abort()
    running = queued = lastKey = undefined
    if (clear) {
      current?.resource.dispose()
      current = undefined
      onChange(null)
      onError(null)
    }
  }
  async function drain() {
    if (!queued || running || stopped) return
    const job = { ...queued, epoch, controller: new AbortController() }
    queued = undefined
    running = job
    job.timer = setTimeout(() => {
      if (running !== job) return
      job.controller.abort()
      running = undefined
      onError(new Error('Image request timed out'))
      void drain()
    }, timeoutMs)
    try {
      const resource = await load(job.url, job.controller.signal)
      if (stopped || job.epoch !== epoch || running !== job) { resource.dispose(); return }
      const previous = current
      current = { ...job, resource }
      onChange({ frame: { ...job.frame, content_type: resource.contentType || job.frame.content_type }, url: resource.url })
      onError(null)
      // Let the reactive view consume the new decoded URL before releasing the old one.
      if (previous) queueMicrotask(() => previous.resource.dispose())
    } catch (error) {
      if (!stopped && job.epoch === epoch && running === job && error?.name !== 'AbortError') onError(error)
    } finally {
      clearTimeout(job.timer)
      if (running === job) { running = undefined; void drain() }
    }
  }
  return {
    request(frame, url, nextScope = 'latest') {
      if (stopped) return
      if (nextScope !== scope) { invalidate(false); scope = nextScope }
      const key = `${frame.id}:${url}`
      if (key === lastKey) return
      lastKey = key
      queued = { frame, url }
      void drain()
    },
    reset() { invalidate(true); scope = undefined },
    dispose() { stopped = true; invalidate(true) }
  }
}
