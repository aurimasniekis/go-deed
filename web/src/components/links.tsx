import { Anchor, Button, type AnchorProps, type ButtonProps } from '@mantine/core'
import { createLink } from '@tanstack/react-router'
import { forwardRef, type AnchorHTMLAttributes } from 'react'

/**
 * Mantine components as type-safe TanStack Router links (route paths, params and search are checked against the route
 * tree; `component={Link}` would lose those types).
 */
const AnchorBase = forwardRef<HTMLAnchorElement, AnchorProps & AnchorHTMLAttributes<HTMLAnchorElement>>(
  function AnchorBase(props, ref) {
    return <Anchor ref={ref} {...props} />
  },
)

const ButtonBase = forwardRef<HTMLAnchorElement, ButtonProps & AnchorHTMLAttributes<HTMLAnchorElement>>(
  function ButtonBase(props, ref) {
    return <Button ref={ref} component="a" {...props} />
  },
)

export const AnchorLink = createLink(AnchorBase)
export const ButtonLink = createLink(ButtonBase)
