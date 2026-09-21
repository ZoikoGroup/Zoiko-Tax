import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
<<<<<<< HEAD
import './index.css'
import App from './App.tsx'

createRoot(document.getElementById('root')!).render(
=======
import { App } from './App'
import './styles.css'

const container = document.getElementById('root')
if (!container) {
  throw new Error('no #root element')
}

createRoot(container).render(
>>>>>>> origin/main
  <StrictMode>
    <App />
  </StrictMode>,
)
