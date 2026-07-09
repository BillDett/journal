import {Node, mergeAttributes} from '@tiptap/core'
import {NodeViewWrapper, ReactNodeViewRenderer, type NodeViewProps} from '@tiptap/react'
import {useEffect, useRef, useState, type PointerEvent as ReactPointerEvent} from 'react'
import {api, messageFromError} from '../wails/libraryApi'

const minImageWidthPercent = 15
const maxImageWidthPercent = 100
type ImageTextAlign = 'left' | 'center' | 'right'

function clampImageWidthPercent(value: unknown) {
  if (typeof value !== 'number' || !Number.isFinite(value)) return null
  return Math.min(maxImageWidthPercent, Math.max(minImageWidthPercent, value))
}

function imageTextAlign(value: unknown): ImageTextAlign {
  return value === 'center' || value === 'right' ? value : 'left'
}

function AttachmentImageView({node, selected, updateAttributes}: NodeViewProps) {
  const attachmentId = String(node.attrs.attachmentId || '')
  const alt = String(node.attrs.alt || '')
  const wrapperRef = useRef<HTMLDivElement | null>(null)
  const [src, setSrc] = useState('')
  const [error, setError] = useState('')
  const [previewWidthPercent, setPreviewWidthPercent] = useState<number | null>(null)
  const widthPercent = previewWidthPercent ?? clampImageWidthPercent(node.attrs.widthPercent)
  const textAlign = imageTextAlign(node.attrs.textAlign)
  const imageStyle = {
    ...(widthPercent === null ? {} : {width: `${widthPercent}%`}),
    ...(textAlign === 'center'
      ? {marginLeft: 'auto', marginRight: 'auto'}
      : textAlign === 'right'
        ? {marginLeft: 'auto', marginRight: 0}
        : {marginLeft: 0, marginRight: 'auto'}),
  }

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

  function startResize(event: ReactPointerEvent<HTMLButtonElement>, side: 'left' | 'right') {
    const wrapper = wrapperRef.current
    const editorPage = wrapper?.closest('.editor-page')
    if (!wrapper || !(editorPage instanceof HTMLElement)) return

    event.preventDefault()
    event.stopPropagation()

    const containerWidth = editorPage.getBoundingClientRect().width
    if (containerWidth <= 0) return

    const startX = event.clientX
    const startWidth = wrapper.getBoundingClientRect().width
    const pointerId = event.pointerId

    event.currentTarget.setPointerCapture(pointerId)
    document.body.classList.add('is-resizing-image')

    const onPointerMove = (moveEvent: PointerEvent) => {
      const delta = side === 'right' ? moveEvent.clientX - startX : startX - moveEvent.clientX
      const width = Math.min(containerWidth, Math.max((containerWidth * minImageWidthPercent) / 100, startWidth + delta))
      const nextPercent = Math.round((width / containerWidth) * 1000) / 10
      setPreviewWidthPercent(nextPercent)
    }

    const finishResize = () => {
      document.removeEventListener('pointermove', onPointerMove)
      document.removeEventListener('pointerup', onPointerUp)
      document.removeEventListener('pointercancel', onPointerCancel)
      document.body.classList.remove('is-resizing-image')
    }

    const onPointerUp = (upEvent: PointerEvent) => {
      finishResize()

      const delta = side === 'right' ? upEvent.clientX - startX : startX - upEvent.clientX
      const width = Math.min(containerWidth, Math.max((containerWidth * minImageWidthPercent) / 100, startWidth + delta))
      const nextPercent = Math.round((width / containerWidth) * 1000) / 10
      setPreviewWidthPercent(null)
      updateAttributes({widthPercent: nextPercent})
    }

    const onPointerCancel = () => {
      finishResize()
      setPreviewWidthPercent(null)
    }

    document.addEventListener('pointermove', onPointerMove)
    document.addEventListener('pointerup', onPointerUp, {once: true})
    document.addEventListener('pointercancel', onPointerCancel, {once: true})
  }

  return (
    <NodeViewWrapper
      ref={wrapperRef}
      className={selected ? 'attachment-image selected' : 'attachment-image'}
      style={imageStyle}
      data-image-width-percent={widthPercent ?? undefined}
      data-text-align={textAlign}
    >
      <div className="attachment-image-frame">
        {src ? <img src={src} alt={alt} draggable={false}/> : <div className="attachment-image-placeholder">{error || 'Loading image...'}</div>}
        <button type="button" className="image-resize-handle left" tabIndex={selected ? 0 : -1} aria-label="Resize image from left" onPointerDown={(event) => startResize(event, 'left')}/>
        <button type="button" className="image-resize-handle right" tabIndex={selected ? 0 : -1} aria-label="Resize image from right" onPointerDown={(event) => startResize(event, 'right')}/>
      </div>
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
      widthPercent: {
        default: null,
        parseHTML: (element) => {
          const value = Number.parseFloat(element.getAttribute('data-image-width-percent') || '')
          return clampImageWidthPercent(value)
        },
        renderHTML: (attributes) => {
          const widthPercent = clampImageWidthPercent(attributes.widthPercent)
          if (widthPercent === null) return {}
          return {
            'data-image-width-percent': String(widthPercent),
            style: `width: ${widthPercent}%;`,
          }
        },
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
