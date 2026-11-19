import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { KeyRound, Loader2, LogIn } from 'lucide-react'
import { useTranslation } from 'react-i18next'
import { getSession, login, setup } from '@/api/client'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { PasswordInput } from '@/components/password-input'
import { AuthLayout } from '../auth-layout'

export function SignIn() {
  const { t } = useTranslation()
  const { redirect } = useSearch({ from: '/(auth)/sign-in' })
  const navigate = useNavigate()
  const session = useQuery({ queryKey: ['session'], queryFn: getSession, staleTime: 0 })
  const [password, setPassword] = useState('')
  const [again, setAgain] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const firstRun = session.data?.setup
  const done = () => navigate({ to: redirect || '/', replace: true })

  if (session.data?.authenticated) void done()

  return (
    <AuthLayout>
      <Card className='w-full max-w-sm gap-4'>
        <CardHeader>
          <CardTitle className='text-lg tracking-tight'>{firstRun ? t('auth.setupTitle') : t('auth.signIn')}</CardTitle>
          <CardDescription>{firstRun ? t('auth.setupDesc') : t('auth.desc')}</CardDescription>
        </CardHeader>
        <CardContent>
          {firstRun && !session.data?.setupAllowed ? (
            <p className='text-sm text-muted-foreground'>{t('auth.setupElsewhere')}</p>
          ) : (
            <form
              className='grid gap-3'
              onSubmit={(e) => {
                e.preventDefault()
                setError('')
                if (firstRun && password !== again) {
                  setError(t('auth.mismatch'))
                  return
                }
                setLoading(true)
                ;(firstRun ? setup(password) : login(password)).then(
                  () => done(),
                  (err: Error) => {
                    setLoading(false)
                    setError(err.message)
                  }
                )
              }}
            >
              <Label htmlFor='password'>{firstRun ? t('auth.newPassword') : t('auth.password')}</Label>
              <PasswordInput id='password' value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
              {firstRun && (
                <>
                  <Label htmlFor='again'>{t('auth.again')}</Label>
                  <PasswordInput id='again' value={again} onChange={(e) => setAgain(e.target.value)} />
                </>
              )}
              {error && <p className='text-sm text-destructive'>{error}</p>}
              <Button className='mt-2' disabled={loading || !password || session.isLoading}>
                {loading ? <Loader2 className='animate-spin' /> : firstRun ? <KeyRound /> : <LogIn />}
                {firstRun ? t('auth.setPassword') : t('auth.signIn')}
              </Button>
              <p className='text-xs text-muted-foreground'>{t('auth.forgot')}</p>
            </form>
          )}
        </CardContent>
      </Card>
    </AuthLayout>
  )
}
