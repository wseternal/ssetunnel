import { Autocomplete, TextField } from '@mui/material';
import type { SavedSession } from './types';

const DEFAULT_TUNNEL = 'http://127.0.0.1:8080';

type Option = string | { label: string; group: string };

interface ServerURLFieldProps {
  value: string;
  onChange: (url: string) => void;
  savedSessions: SavedSession[];
  label?: string;
  helperText?: string;
  fullWidth?: boolean;
  sx?: Record<string, unknown>;
}

export default function ServerURLField({
  value,
  onChange,
  savedSessions,
  label = 'Server URL',
  helperText,
  fullWidth = true,
  sx,
}: ServerURLFieldProps) {
  const defaultOpt: Option = { label: DEFAULT_TUNNEL, group: 'Default' };
  const options: Option[] = [
    defaultOpt,
    ...savedSessions.map(s => ({ label: s.server_url, group: 'Saved Sessions' })),
  ];

  const currentOpt: Option = options.find(
    o => (typeof o === 'string' ? o : o.label) === value,
  ) || value;

  return (
    <Autocomplete
      freeSolo
      disableClearable
      options={options}
      groupBy={opt => (typeof opt === 'string' ? '' : opt.group)}
      getOptionLabel={opt => (typeof opt === 'string' ? opt : opt.label)}
      value={currentOpt}
      onChange={(_, v) => {
        if (v !== null) onChange(typeof v === 'string' ? v : v.label);
      }}
      renderInput={params => (
        <TextField {...params} fullWidth={fullWidth} label={label} helperText={helperText} />
      )}
      sx={sx}
    />
  );
}

export { DEFAULT_TUNNEL };
