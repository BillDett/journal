// Coordinates client-side operations that can otherwise complete out of order.
// It is framework-agnostic so UI components can share the same semantics.
export class OperationCoordinator {
  private treeRequestVersion = 0
  private documentRequestVersion = 0
  private active = new Set<string>()

  nextTreeRequest() {
    this.treeRequestVersion += 1
    return this.treeRequestVersion
  }

  isCurrentTreeRequest(version: number) {
    return version === this.treeRequestVersion
  }

  invalidateTreeRequests() {
    this.treeRequestVersion += 1
  }

  nextDocumentRequest() {
    this.documentRequestVersion += 1
    return this.documentRequestVersion
  }

  isCurrentDocumentRequest(version: number) {
    return version === this.documentRequestVersion
  }

  begin(key: string) {
    if (this.active.has(key)) return false
    this.active.add(key)
    return true
  }

  end(key: string) {
    this.active.delete(key)
  }
}
