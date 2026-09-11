export type CRDTUpdate = {id: string, data: string}
export type CRDTProjection<T> = {content: T, snapshot: string, stateVector: string}

type PersistenceOptions<T> = {
  throughSeq: number
  submit: (updates: CRDTUpdate[]) => Promise<{throughSeq: number}>
  materialize: (projection: CRDTProjection<T> & {throughSeq: number}) => Promise<void>
  capture: () => CRDTProjection<T>
  onSaved: (content: T) => void
  onError: (error: unknown) => void
}

// One queue owns submissions and projections. A flush is a barrier over earlier
// requests, including a request whose updates have left the pending array.
export class CRDTPersistence<T> {
  private pending: CRDTUpdate[] = []
  private tail: Promise<void> = Promise.resolve()
  private revision = 0
  private throughSeq: number
  private persistTimer: ReturnType<typeof setTimeout> | undefined
  private projectionTimer: ReturnType<typeof setTimeout> | undefined
  private disposed = false
  private options: PersistenceOptions<T>

  constructor(options: PersistenceOptions<T>) {
    this.options = options
    this.throughSeq = options.throughSeq
  }

  enqueue(update: CRDTUpdate) {
    if (this.disposed) throw new Error('CRDT persistence is closed')
    this.pending.push(update)
    this.revision += 1
    // Bound persistence latency during continuous typing; do not debounce this
    // timer or share it with the more expensive projection/compaction timer.
    if (this.persistTimer === undefined) {
      this.persistTimer = setTimeout(() => {
        this.persistTimer = undefined
        void this.persist().catch(this.options.onError)
      }, 80)
    }
  }

  scheduleProjection(intervalMs: number) {
    if (this.disposed) return
    clearTimeout(this.projectionTimer)
    this.projectionTimer = setTimeout(() => {
      this.projectionTimer = undefined
      void this.flush().catch(this.options.onError)
    }, intervalMs)
  }

  private serialize(operation: () => Promise<void>) {
    const result = this.tail.then(operation)
    // Keep the queue usable after a failure. Failed updates remain pending and
    // a later flush retries them with their original idempotency IDs.
    this.tail = result.catch(() => {})
    return result
  }

  private async submit(updates: CRDTUpdate[]) {
    if (updates.length === 0) return
    try {
      const response = await this.options.submit(updates)
      this.throughSeq = Math.max(this.throughSeq, response.throughSeq)
    } catch (error) {
      this.pending.unshift(...updates)
      throw error
    }
  }

  persist() {
    return this.serialize(() => this.submit(this.pending.splice(0)))
  }

  flush() {
    this.cancelTimers()
    return this.serialize(async () => {
      for (;;) {
        // Capture the update batch and its exact projection synchronously.
        // Edits made during IPC must never be labelled with an earlier ack.
        const revision = this.revision
        const projection = this.options.capture()
        await this.submit(this.pending.splice(0))
        await this.options.materialize({...projection, throughSeq: this.throughSeq})
        if (revision === this.revision) {
          this.options.onSaved(projection.content)
          return
        }
        // A navigation/close flush also covers edits arriving during the save.
      }
    })
  }

  private cancelTimers() {
    clearTimeout(this.persistTimer)
    clearTimeout(this.projectionTimer)
    this.persistTimer = undefined
    this.projectionTimer = undefined
  }

  dispose() {
    this.disposed = true
    this.cancelTimers()
  }
}
