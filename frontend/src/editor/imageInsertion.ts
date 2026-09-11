import type {Editor} from '@tiptap/react'

type UploadedImage = {id: string, originalName: string}
type ImageInsertionOptions = {
  getEditor: () => Editor | null
  upload: (file: File, isCurrent: () => boolean) => Promise<UploadedImage | null>
  onError: (error: unknown) => void
}

// DOM handlers can outlive the render that created them, and uploads span
// editor/document changes. Resolve the live editor at the event, then keep the
// whole batch tied to that instance rather than redirecting it to a new one.
export async function insertImageAttachments(files: File[], options: ImageInsertionOptions, position?: number) {
  const editor = options.getEditor()
  if (!editor) return
  const isCurrent = () => options.getEditor() === editor && !editor.isDestroyed && editor.isEditable
  let insertPosition = position
  for (const file of files) {
    if (!isCurrent()) return
    try {
      const attachment = await options.upload(file, isCurrent)
      if (!isCurrent()) return
      if (!attachment?.id) continue
      const command = editor.chain().focus()
      if (typeof insertPosition === 'number') {
        // Other edits may have shortened the document while the upload ran.
        insertPosition = Math.max(0, Math.min(insertPosition, editor.state.doc.content.size))
        command.setTextSelection(insertPosition)
      }
      const inserted = command.insertContent({
        type: 'attachmentImage',
        attrs: {attachmentId: attachment.id, alt: attachment.originalName || file.name},
      }).run()
      if (inserted && typeof insertPosition === 'number') insertPosition += 1
    } catch (error) {
      if (!isCurrent()) return
      options.onError(error)
    }
  }
}
