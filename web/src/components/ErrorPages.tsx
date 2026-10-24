import { ButtonLink } from '@/components/links'
import { Alert, Button, Center, Code, Group, Stack, Text, Title } from '@mantine/core'
import { IconAlertTriangle, IconMapOff } from '@tabler/icons-react'
import { useRouter, type ErrorComponentProps } from '@tanstack/react-router'
import { errorMessage } from '@/api/client'
import { BrandMark } from './Brand'

export function NotFoundPage() {
  return (
    <Center mih="60vh" p="md" component="main">
      <Stack align="center" gap="sm" maw={460} ta="center">
        <BrandMark size={36} />
        <IconMapOff size={28} stroke={1.5} aria-hidden />
        <Title order={1}>Page not found</Title>
        <Text c="dimmed" size="sm">
          This address does not match any page in Deed. It may have moved, or the link is incomplete.
        </Text>
        <Group>
          <ButtonLink to="/">Go to the dashboard</ButtonLink>
          <ButtonLink to="/inventory" variant="default">
            Open inventory
          </ButtonLink>
        </Group>
      </Stack>
    </Center>
  )
}

/** Route-level error boundary: a render or loader error shows here instead of a blank page. */
export function RouteErrorPage({ error, reset }: ErrorComponentProps) {
  const router = useRouter()
  return (
    <Center mih="50vh" p="md">
      <Stack maw={560} w="100%" gap="sm">
        <Alert
          color="red"
          icon={<IconAlertTriangle size={18} />}
          title="Something went wrong on this page"
          role="alert"
        >
          <Stack gap="xs">
            <Text size="sm">The page hit an unexpected error. Your data is safe; reloading usually helps.</Text>
            <Code block style={{ whiteSpace: 'pre-wrap', fontSize: 12 }}>
              {errorMessage(error)}
            </Code>
          </Stack>
        </Alert>
        <Group>
          <Button
            onClick={() => {
              reset()
              void router.invalidate()
            }}
          >
            Try again
          </Button>
          <ButtonLink to="/" variant="default">
            Dashboard
          </ButtonLink>
        </Group>
      </Stack>
    </Center>
  )
}
