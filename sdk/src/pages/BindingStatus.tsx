import React, { useEffect, useState } from 'react';
import { Card, CardContent, Typography, Table, TableBody, TableCell, TableContainer, TableHead, TableRow, Paper, Chip, CircularProgress, Box } from '@mui/material';
import { fetchBindings, type PlayerBinding } from '../api/binding';
import { useSdkTranslation } from '../hooks/useSdkTranslation';
// @ts-ignore
import { getUid } from '../utils/proxy';

export default function BindingStatus() {
  const { t } = useSdkTranslation();
  const [bindings, setBindings] = useState<PlayerBinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const uid = getUid();
    if (!uid) {
      setError('Not logged in');
      setLoading(false);
      return;
    }

    fetchBindings(uid)
      .then((res) => {
        if (res.code === 200 && res.data) {
          setBindings(res.data);
        } else {
          setError(res.message || 'Failed to load');
        }
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  return (
    <Card>
      <CardContent>
        <Typography variant="h6" gutterBottom>
          {t('binding.title')}
        </Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
          {t('binding.description')}
        </Typography>

        {loading && (
          <Box display="flex" justifyContent="center" py={4}>
            <CircularProgress />
          </Box>
        )}

        {error && (
          <Typography color="error">{error}</Typography>
        )}

        {!loading && !error && bindings.length === 0 && (
          <Typography color="text.secondary">{t('binding.notBound')}</Typography>
        )}

        {!loading && !error && bindings.length > 0 && (
          <TableContainer component={Paper} variant="outlined">
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>{t('binding.playerName')}</TableCell>
                  <TableCell>{t('binding.status')}</TableCell>
                  <TableCell>{t('binding.quickAuth')}</TableCell>
                  <TableCell>{t('binding.lastLogin')}</TableCell>
                  <TableCell>{t('binding.gameUUID')}</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {bindings.map((b) => (
                  <TableRow key={b.gameUUID}>
                    <TableCell>{b.playerName}</TableCell>
                    <TableCell>
                      <Chip
                        label={b.cbh === 1 ? t('binding.claimed') : t('binding.guest')}
                        color={b.cbh === 1 ? 'success' : 'default'}
                        size="small"
                      />
                    </TableCell>
                    <TableCell>{b.quickAuth ? '✓' : '—'}</TableCell>
                    <TableCell>{new Date(b.lastLogin).toLocaleString()}</TableCell>
                    <TableCell>
                      <Typography variant="caption" fontFamily="monospace">
                        {b.gameUUID}
                      </Typography>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        )}
      </CardContent>
    </Card>
  );
}