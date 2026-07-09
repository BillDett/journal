import CharacterCount from '@tiptap/extension-character-count'
import Highlight from '@tiptap/extension-highlight'
import Link from '@tiptap/extension-link'
import Placeholder from '@tiptap/extension-placeholder'
import TableCell from '@tiptap/extension-table-cell'
import TableHeader from '@tiptap/extension-table-header'
import TableRow from '@tiptap/extension-table-row'
import TaskItem from '@tiptap/extension-task-item'
import TaskList from '@tiptap/extension-task-list'
import TextAlign from '@tiptap/extension-text-align'
import Typography from '@tiptap/extension-typography'
import Underline from '@tiptap/extension-underline'
import StarterKit from '@tiptap/starter-kit'
import {AttachmentImage} from './attachmentImage'
import {BlockIndent} from './blockIndent'
import {ListNormalization} from './listNormalization'
import {ResizableTable} from './resizableTable'

export const editorExtensions = [
  StarterKit.configure({
    heading: {
      levels: [1, 2, 3, 4, 5, 6],
    },
  }),
  BlockIndent,
  ListNormalization,
  AttachmentImage,
  Link.configure({
    autolink: true,
    defaultProtocol: 'https',
    openOnClick: false,
  }),
  Underline,
  Highlight.configure({
    multicolor: true,
  }),
  TaskList,
  TaskItem.configure({
    nested: true,
  }),
  ResizableTable.configure({
    resizable: true,
    cellMinWidth: 56,
    handleWidth: 6,
    lastColumnResizable: false,
  }),
  TableRow,
  TableHeader,
  TableCell,
  Placeholder.configure({
    placeholder: ({node}) => {
      if (node.type.name === 'heading') return 'Heading'
      return ''
    },
  }),
  Typography,
  TextAlign.configure({
    types: ['heading', 'paragraph'],
  }),
  CharacterCount,
]
