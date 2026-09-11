# Journal requirements

Journal is a small web application that runs on MacOS and Linux machines. It provides a basic word processing function and self contained file management abilities. 

The UI is split into three main areas:
1. A large window taking up most of the center/right containing the word processor.
2. A narrow outliner taking up the left side of the window.
3. A toolbar/menu bar across the top of the window.

Journal maintains all of its files as ProseMirror JSON documents stored in a sqlite database. Each document has a title (which is editable at any time).

Creating a new document (via the toolbar or menus) opens a blank document called "Untitled" in the editor pane and creates an entry in the database. The user can double click the document title to change it or begin typing. Changes are automatically saved to the database behind the scenes. Users do not have to explicitly save their work.

In the outline view, a list of documents is shown. Clicking on a document name opens it up in the editor. If there are pending changes in the editor, those are first saved before opening the document.

The outline supports the concept of folders (which are created via the toolbar or menus). This lets documents be stored in a hierarchical manner inside the database. Documents can be dragged between folders. Documents can also be deleted via the outliner (with the appropriate confirmation window). Folders can also be renamed or deleted. Deleting a Folder removes all of its contained sub-folders and documents. There is a system defined folder called "Trash" which cannot be removed or deleted. Any user created folder or document that is deleted by the user is simply moved to the Trash folder. Deleting a document or Folder from Trash removes it permanently (again, after confirmation). It is also possible to drag documents and folders out of Trash back to another folder (or top level) in the outliner.

Another major feature of Journal is the ability to search across all documents. This is a feature of the outliner. Clicking the search icon in the outliner lets the user specify text- as typing happens the outline is automatically filtered to only show folders or documents containing that text. Clearing the search filter text should restore the entire folder hierarchy in the outliner.


