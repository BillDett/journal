import {Node, mergeAttributes} from '@tiptap/core'
import {NodeViewWrapper, ReactNodeViewRenderer, type NodeViewProps} from '@tiptap/react'
import {useEffect, useState} from 'react'
import {api, messageFromError} from '../wails/libraryApi'

function AttachmentImageView({node, selected}: NodeViewProps) {
  const attachmentId = String(node.attrs.attachmentId || '')
  const alt = String(node.attrs.alt || '')
  const [src, setSrc] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    setSrc('')
    setError('')

    if (!attachmentId) {
      setError('Missing image')
      return () => {
        active = false
      }
    }

    void api.GetDocumentAttachmentDataURL(attachmentId)
      .then((response) => {
        if (!active) return
        setSrc(response.dataUrl)
      })
      .catch((error) => {
        if (!active) return
        setError(messageFromError(error))
      })

    return () => {
      active = false
    }
  }, [attachmentId])

  return (
    <NodeViewWrapper className={selected ? 'attachment-image selected' : 'attachment-image'}>
      {src ? <img src={src} alt={alt}/> : <div className="attachment-image-placeholder">{error || 'Loading image...'}</div>}
    </NodeViewWrapper>
  )
}

export const AttachmentImage = Node.create({
  name: 'attachmentImage',
  group: 'block',
  atom: true,
  draggable: true,
  selectable: true,

  addAttributes() {
    return {
      attachmentId: {
        default: '',
        parseHTML: (element) => element.getAttribute('data-attachment-id') || '',
        renderHTML: (attributes) => ({'data-attachment-id': attributes.attachmentId}),
      },
      alt: {
        default: '',
        parseHTML: (element) => element.getAttribute('alt') || '',
        renderHTML: (attributes) => ({alt: attributes.alt || ''}),
      },
    }
  },

  parseHTML() {
    return [{tag: 'img[data-attachment-id]'}]
  },

  renderHTML({HTMLAttributes}) {
    return ['img', mergeAttributes(HTMLAttributes)]
  },

  addNodeView() {
    return ReactNodeViewRenderer(AttachmentImageView)
  },
})
