import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import '@mantine/core/styles.css'
import '@mantine/notifications/styles.css'
import '@mantine/spotlight/styles.css'
import '@xyflow/react/dist/style.css'
import './theme/global.css'

import { MantineProvider } from '@mantine/core'
import { Notifications } from '@mantine/notifications'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { Devtools } from './devtools'
import { cspNonce } from './lib/nonce'
import { queryClient, router } from './router'
import { cssVariablesResolver, theme } from './theme/theme'

const nonce = cspNonce()

const root = document.getElementById('root')
if (root) {
  createRoot(root).render(
    <StrictMode>
      <MantineProvider
        theme={theme}
        defaultColorScheme="auto"
        cssVariablesResolver={cssVariablesResolver}
        getStyleNonce={() => nonce}
      >
        <Notifications position="bottom-right" limit={4} />
        <QueryClientProvider client={queryClient}>
          <RouterProvider router={router} />
          <Devtools />
        </QueryClientProvider>
      </MantineProvider>
    </StrictMode>,
  )
}
